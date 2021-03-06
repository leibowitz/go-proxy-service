package main

import (
	"flag"
	"fmt"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"log"
	"net/http"
	"strconv"
	//"regexp"
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leibowitz/goproxy"
)

type ContextUserData struct {
	Store bool
	Time  int64
	Body  io.Reader
	ReqId bson.ObjectId
	//Body   []byte
	Header              http.Header
	Origin              string
	ElapsedTime         float32
	SaveAsDocumentation bool // Set to true if we have to record this into doc collection
}

type Content struct {
	//Id       bson.ObjectId
	Request    Request   "request"
	Response   Response  "response"
	Date       time.Time "date"
	SocketUUID []byte    "uuid"
}

type Origin struct {
	DefaultIgnore bool "filterAll"
}

type Rewrite struct {
	Host      string "host"
	DHost     string "dhost"
	Protocol  string "protocol"
	DProtocol string "dprotocol"
}

type Rule struct {
	//Id       bson.ObjectId
	Active     bool        "active"
	Dynamic    bool        "dynamic"
	Host       string      "host"
	Path       string      "path"
	Query      string      "query"
	Method     string      "method"
	Status     string      "status"
	Response   string      "response"
	Body       string      "body"
	ReqBody    string      "reqbody"
	ReqHeader  http.Header "reqheaders"
	RespHeader http.Header "respheaders"
	Origin     string      "origin"
	Delay      int32       "delay"
}

type Request struct {
	Origin string "origin"
	Body   string "body"
	FileId bson.ObjectId
	Query  string "query"
	//Date    time.Time   "date"
	Host    string      "host"
	Scheme  string      "scheme"
	Url     string      "url"
	Method  string      "method"
	Path    string      "path"
	Time    float32     "time"
	Headers http.Header "headers"
}

type Response struct {
	FileId  bson.ObjectId
	Body    string      "body"
	Headers http.Header "headers"
	Status  int         "status"
}

func NewResponse(r *http.Request, headers http.Header, status int, body io.ReadCloser) *http.Response {
	resp := &http.Response{}
	resp.Request = r
	resp.TransferEncoding = r.TransferEncoding
	resp.Header = headers
	resp.StatusCode = status
	//resp.ContentLength = int64(buf.Len())
	resp.Body = body
	return resp
}

type IgnoreHost struct {
	Active bool     "active"
	Host   string   "host"
	Paths  []string "paths"
}

func Contains(hosts []string, host string) bool {
	for _, h := range hosts {
		if host == h {
			return true
		}
	}
	return false
}

func main() {
	verbose := flag.Bool("v", false, "should every proxy request be logged to stdout")
	mongourl := flag.String("mongourl", "", "record request/response in mongodb")
	mock := flag.Bool("m", false, "send fake responses")
	addr := flag.String("l", ":8080", "on which address should the proxy listen")

	flag.Parse()

	tmpdir := filepath.Join(os.TempDir(), "proxy-service")

	if _, err := os.Stat(tmpdir); err != nil {
		if os.IsNotExist(err) {
			// Create temp directory to store body response
			err = os.MkdirAll(tmpdir, 0777)
		}

		// err should be nil if we just created the directory
		if err != nil {
			panic(err)
		}
	}

	db := new(mgo.Database)
	c := new(mgo.Collection)
	h := new(mgo.Collection)
	rules := new(mgo.Collection)
	ignores := new(mgo.Collection)
	origins := new(mgo.Collection)
	doc := new(mgo.Collection)
	docsettings := new(mgo.Collection)

	if len(*mongourl) != 0 {
		log.Printf("Connecting to mongodb %s", *mongourl)
		// Mongo DB connection
		info, err := mgo.ParseURL(*mongourl)
		if err != nil {
			panic(err)
		}
		info.Mechanism = "SCRAM-SHA-1"
		session, err := mgo.DialWithInfo(info)
		if err != nil {
			panic(err)
		}
		defer session.Close()

		// Optional. Switch the session to a monotonic behavior.
		session.SetMode(mgo.Monotonic, true)

		db = session.DB("proxyservice")
		c = db.C("log_logentry")
		// Make sure the log_logentry is created as a capped collection
		ccinfo := mgo.CollectionInfo{
			Capped:   true,
			MaxBytes: 5242880, // 5MB
		}

		// try creating the collection
		if err := c.Create(&ccinfo); err != nil {
			//panic(err)
		}

		h = db.C("log_hostrewrite")
		rules = db.C("log_rules")
		ignores = db.C("log_ignores")
		origins = db.C("origins")
		doc = db.C("documentation")
		docsettings = db.C("docsettings")

	} else {
		db = nil
		c = nil
		h = nil
		rules = nil
		ignores = nil
		origins = nil
		doc = nil
		docsettings = nil
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = *verbose

	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		ctx.UserData = nil

		var origin string
		ctx.Logf("headers: %v", req.Header)
		if origin = req.Header.Get("X-Forwarded-For"); origin == "" {
			origin = ipAddrFromRemoteAddr(ctx.Req.RemoteAddr)
		}
		ctx.Logf("Origin: %s", origin)
		/*ctx.RoundTripper = goproxy.RoundTripperFunc(func (req *http.Request, ctx *goproxy.ProxyCtx) (resp *http.Response, err error) {
			//data := transport.RoundTripDetails{}
			data, resp, err := tr.DetailedRoundTrip(req)
			//log.Printf("%+v", data)
			return
		})*/

		method := req.Method
		// Sanity check for appboy request from iOS Sdk sending incorrect requests
		if len(method) > 10 {
			ctx.Logf("Method is too long: %s", method)
			valid := []string{"POST", "GET", "PATCH", "PUT", "DELETE", "OPTIONS", "TRACE", "HEAD", "CONNECT"}
			for _, m := range valid {
				if method[len(method)-len(m):len(method)] == m {
					method = m
					break
				}
			}

			// Could not detect valid method, do not store this request
			if method == req.Method {
				return req, nil
			}
			// Override method
			req.Method = method
		}

		rewrite := Rewrite{}
		if h != nil && h.Database != nil {
			err := h.Find(bson.M{"host": req.Host, "active": true}).One(&rewrite)
			if err == nil {
				if rewrite.DProtocol != "" {
					req.URL.Scheme = rewrite.DProtocol
				}
				req.URL.Host = rewrite.DHost
				req.Host = rewrite.DHost
				ctx.Logf("Rewrite: %+v, URL: %+v", rewrite, req.URL)
			}
		}

		var record bool
		// Ignore hosts
		if ignores != nil {
			iter := ignores.Find(bson.M{"host": req.Host}).Sort("-paths").Iter()
			var ignoreHost IgnoreHost
			for iter.Next(&ignoreHost) {
				// If we have a path and we want to record this, then skip the other checks
				if Contains(ignoreHost.Paths, req.URL.Path) && ignoreHost.Active {
					record = true
					break
				}
				if len(ignoreHost.Paths) == 0 || Contains(ignoreHost.Paths, req.URL.Path) {
					// If we don't want to record this host or this path, skip it
					if !ignoreHost.Active {
						ctx.Logf("Not recording: %v, %v", ignoreHost, ctx)
						err := iter.Close()
						if err != nil {
							ctx.Warnf("Unable to check if request should be recorded: %s", err)
						}
						return req, nil
					} else {
						record = true
					}
				}
			}
			err := iter.Close()

			if err != nil {
				ctx.Warnf("Unable to check if request should be recorded: %s", err)
			}
		}

		// If we didn't find a config for this host/path
		if !record {
			var settings Origin
			err := origins.Find(bson.M{"origin": origin}).One(&settings)
			if err != nil {
				ctx.Warnf("Unable to check origin settings: %s", err)
			}

			// Do not record this request if the default is to ignore
			if settings.DefaultIgnore {
				return req, nil
			}
		}

		//log.Printf("%+v", getHost(req.RemoteAddr))
		/*if ctx.UserData != nil {
			from = ctx.UserData.(*transport.RoundTripDetails).TCPAddr.String()
		}*/

		//log.Printf("Request: %s %s %s", req.Method, req.Host, req.RequestURI)

		//host := req.Host
		// Request to domain--name-co-uk.mocky.dev
		// will be forwarded to domain-name.co.uk
		/*if strings.Contains(host, ".mocky.dev") {
			host = strings.Replace(host, ".mocky.dev", "", 1)
			host = strings.Replace(host, "-", ".", -1)
			host = strings.Replace(host, "..", "-", -1)
		} else if strings.Contains(host, ".proxy.dev") {
			host = strings.Replace(host, ".proxy.dev", "", 1)
			host = strings.Replace(host, "-", ".", -1)
			host = strings.Replace(host, "..", "-", -1)
		}*/

		/*r, _ := regexp.Compile(".([0-9]+)$")
		// Check if host is hostname.80 (host with port number)
		res := r.FindStringSubmatch(host)
		if res != nil && len(res[1]) != 0 {
			host = strings.Replace(host, strings.Join([]string{".", res[1]}, ""), "", 1)
			host = strings.Join([]string{host, res[1]}, ":")
			log.Printf("Changing host to %v", host);
		}*/

		//log.Printf("Target Host: %s - Headers: %+v", host, req.Header)
		//req.Host = host

		//log.Printf("%+v", req)

		var reqbody []byte

		var bodyreader io.Reader
		if rules != nil && rules.Database != nil && *mock && req.Method != "CONNECT" {
			//reqbody := string(body[:])
			//log.Printf("request body: %s", reqbody)
			rule := Rule{}
			//ctx.Logf("Looking for existing request")
			/*fmt.Println("RequestURI:", req.RequestURI)
			  fmt.Println("Path:", req.URL.Path)
			  fmt.Println("Host:", req.Host)
			  fmt.Println("Method:", req.Method)*/
			b := bson.M{"$and": []bson.M{
				bson.M{"active": true},
				//bson.M{"dynamic": false},
				bson.M{"origin": bson.M{"$in": []interface{}{origin, false}}},
				bson.M{"host": bson.M{"$in": []interface{}{req.Host, false}}},
				bson.M{"method": bson.M{"$in": []interface{}{req.Method, false}}},
				bson.M{"path": bson.M{"$in": []interface{}{req.URL.Path, false}}},
				bson.M{"query": bson.M{"$in": []interface{}{req.URL.Query().Encode(), false}}},
			}}

			//b := bson.M{"active": true, "dynamic": false, "host": req.Host, "method": req.Method, "path": req.URL.Path, "query": req.URL.Query().Encode()}
			ctx.Logf("Looking for a rule for %s", req.URL.Path)
			err := rules.Find(b).Sort("dynamic").One(&rule)
			//log.Printf("Query: %+v, Res: %+v", b, rule)
			if err == nil {
				ctx.Logf("Found rule: %+v", rule)
				status, err := strconv.Atoi(rule.Status)
				if rule.Dynamic && c != nil && c.Database != nil {
					result := Content{}
					reqQuery := bson.M{"$and": []bson.M{
						/*bson.M{"origin": bson.M{"$in": []interface{}{origin, false}},
						},*/
						bson.M{"request.host": bson.M{"$in": []interface{}{req.Host}}},
						bson.M{"request.method": bson.M{"$in": []interface{}{req.Method}}},
						bson.M{"request.path": bson.M{"$in": []interface{}{req.URL.Path}}},
						bson.M{"response.status": bson.M{"$in": []interface{}{status}}},
						/*bson.M{"query": bson.M{"$in": []interface{}{req.URL.Query().Encode()}},
						},*/
					}}
					ctx.Logf("Query %+v", reqQuery)
					//reqQuery := bson.M{"request.host": rule.Host, "request.method": rule.Method, "response.status": status, "request.path": rule.Path}
					err = c.Find(reqQuery).Sort("-date").One(&result)
					if err == nil && db != nil {
						ctx.Logf("Found a dynamic rule matching, returning it: %+v", result)
						respId := result.Response.FileId
						//reqfile, _ := getMongoFileContent(ctx, *db, result.Request.FileId)
						respfile, err := getMongoFileContent(ctx, *db, respId)
						if respfile != nil && err == nil {
							//reqbody := ioutil.NopCloser(bytes.NewBufferString(rule.ReqBody))
							//respbody := ioutil.NopCloser(bytes.NewBufferString(rule.Body))
							ctx.Logf("Header: %+v", result.Response.Headers)

							resp := NewResponse(req, result.Response.Headers, status, respfile)
							ctx.UserData = ContextUserData{Store: true, Time: 0, Body: req.Body, Header: req.Header, Origin: origin}
							return req, resp
						} else {
							ctx.Logf("Couldn't retrieve the response body: %+v", err)
						}
					} else {
						ctx.Logf("Couldn't find a dynamic response matching: %+v", err)
					}
				} else {
					reqbody := ioutil.NopCloser(bytes.NewBufferString(rule.ReqBody))
					respbody := ioutil.NopCloser(bytes.NewBufferString(rule.Body))
					ctx.Logf("Found a static rule matching, returning it: %+v", rule.Response)
					resp := NewResponse(req, rule.RespHeader, status, respbody)
					ctx.Delay = rule.Delay
					ctx.UserData = ContextUserData{Store: true, Time: 0, Body: reqbody, Header: req.Header, Origin: origin}
					return req, resp
				}
				/*result := Content{}
				  err = c.Find(bson.M{"_id": bson.ObjectIdHex(rule.Response)}).One(&result)*/
				//err := c.Find(bson.M{"request.host": req.Host, "request.method": req.Method, "response.status": 200, "request.path": req.URL.Path}).Sort("-date").One(&result)
				if err == nil {
					//log.Printf("Found %+v", result)
					//respbody := result.Response.Body
					/*file, err := getMongoFileContent(*db, result.Response.FileId)
					    if err == nil {
						resp := NewResponse(req, result.Response.Headers.Get("Content-Type"), result.Response.Status, file)
						ctx.UserData = ContextUserData{Store: false, Time: 0, Header: result.Request.Headers, Origin: origin}
						return req, resp
					    }*/
					//ctx.Logf("Found one")
					/*fmt.Println("Path:", result.Request.Path)
					  //fmt.Println("Body:", result.Request.Body)
					  fmt.Println("Method:", result.Request.Method)
					  fmt.Println("Host:", result.Request.Host)
					  fmt.Println("Time:", result.Request.Time)
					  fmt.Println("Date:", result.Request.Date)
					  fmt.Println("Headers:", result.Request.Headers)

					  //fmt.Println("Body:", result.Response.Body)
					  fmt.Println("Status:", result.Response.Status)
					  fmt.Println("Headers:", result.Response.Headers)*/

					//resp := goproxy.NewResponse(req, result.Response.Headers.Get("Content-Type"), result.Response.Status, result.Response.Body)
					//ctx.UserData = ContextUserData{Store: false, Time: 0}
					//return req, resp
				}
			}

			// read the whole body
			reqbody, err = ioutil.ReadAll(req.Body)
			if err != nil {
				ctx.Warnf("Cannot read request body %s", err)
			}

			defer req.Body.Close()
			req.Body = ioutil.NopCloser(bytes.NewBuffer(reqbody))

			bodyreader = bytes.NewReader(reqbody)

		} else {
			bodyreader = req.Body
		}

		ctx.UserData = ContextUserData{Store: true, Time: time.Now().UnixNano(), Body: bodyreader, Header: req.Header, Origin: origin}
		return req, nil
	})

	proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		//ctx.Logf("Method: %s - host: %s", ctx.Resp.Request.Method, ctx.Resp.Request.Host)
		if c != nil && c.Database != nil && ctx.UserData != nil && ctx.UserData.(ContextUserData).Store && ctx.Resp != nil && ctx.Resp.Request != nil && ctx.Resp.Request.Method != "CONNECT" && db != nil {
			// get response content type
			respctype := getContentType(ctx.Resp.Header.Get("Content-Type"))

			//log.Printf("Resp Contenttype %s", respctype)

			respid := bson.NewObjectId()
			//log.Printf("Resp id: %s, host: %s", respid.Hex(), ctx.Resp.Request.Host)

			filename := filepath.Join(tmpdir, respid.Hex())

			reqid := bson.NewObjectId()

			userData := ctx.UserData.(ContextUserData)
			userData.ReqId = reqid
			userData.ElapsedTime = float32(time.Now().UnixNano()-ctx.UserData.(ContextUserData).Time) / 1.0e9

			count, err := docsettings.Find(bson.M{"host": ctx.Resp.Request.Host, "active": true}).Count()
			if err != nil {
				ctx.Warnf("Unable to check doc settings for host %s", ctx.Resp.Request.Host)
			} else if count != 0 {
				// Look for a record for this host/path and method
				b := bson.M{"$and": []bson.M{
					bson.M{"request.host": ctx.Resp.Request.Host},
					bson.M{"request.method": ctx.Resp.Request.Method},
					bson.M{"request.path": ctx.Resp.Request.URL.Path},
					bson.M{"response.status": ctx.Resp.StatusCode},
				}}

				ctx.Logf("Looking for a record for [%s-%d] %s%s", ctx.Resp.Request.Method, ctx.Resp.StatusCode, ctx.Resp.Request.Host, ctx.Resp.Request.URL.Path)
				count, err := doc.Find(b).Count()

				if err != nil {
					ctx.Warnf("Unable to query doc collection: %s", err)
				} else if count == 0 {
					// No record found, store it!
					userData.SaveAsDocumentation = true
				}
			}

			ctx.UserData = userData

			//log.Printf("Duplicating Body file id: %s", respid.String())
			fs, err := NewFileStream(filename, *db, respctype, respid, ctx)
			if err != nil {
				ctx.Logf("Unable to create file: %s", err.Error())
			}

			reqctype := getContentType(ctx.Resp.Request.Header.Get("Content-Type"))

			//log.Printf("Req Contenttype %s", reqctype)

			if reqctype == "application/x-www-form-urlencoded" {
				//log.Printf("setting req content type to text/plain for saving to mongo")
				reqctype = "text/plain"
			}

			//log.Printf("Req id: %s, host: %s", reqid.Hex(), ctx.Resp.Request.Host)

			err = saveFileToMongo(*db, reqid, reqctype, ctx.UserData.(ContextUserData).Body, reqid.Hex(), ctx)
			if err != nil {
				ctx.Warnf("Unable to save file to mongo: %s", err)
			}

			// prepare document
			content := Content{
				//Id: docid,
				Request: Request{
					Origin:  ctx.UserData.(ContextUserData).Origin,
					Path:    ctx.Resp.Request.URL.Path,
					Query:   ctx.Resp.Request.URL.Query().Encode(),
					FileId:  reqid,
					Url:     ctx.Resp.Request.URL.String(),
					Scheme:  ctx.Resp.Request.URL.Scheme,
					Host:    ctx.Resp.Request.Host,
					Method:  ctx.Resp.Request.Method,
					Time:    ctx.UserData.(ContextUserData).ElapsedTime,
					Headers: ctx.UserData.(ContextUserData).Header},
				Response: Response{
					Status:  ctx.Resp.StatusCode,
					Headers: ctx.Resp.Header,
					FileId:  respid},
				SocketUUID: ctx.Uuid.Bytes(),
				Date:       time.Now(),
			}

			err = c.Insert(content)
			if err != nil {
				ctx.Logf("Can't insert document: %v", err)
			} else {
				ctx.Logf("MongoDB document saved: %+v", content)
			}

			if fs != nil {
				resp.Body = NewTeeReadCloser(resp.Body, fs)
			}

		}

		return resp
	})

	log.Printf("Starting Proxy %+v", *addr)

	log.Fatalln(http.ListenAndServe(*addr, proxy))
}

func saveAsDoc(fs *FileStream) error {
	ctx := fs.ctx
	ctx.Logf("Saving as documentation")
	if ctx.UserData == nil {
		return fmt.Errorf("UserData is nil")
	}
	doc := fs.db.C("documentation")
	content := Content{
		Request: Request{
			Origin:  ctx.UserData.(ContextUserData).Origin,
			Path:    ctx.Resp.Request.URL.Path,
			Query:   ctx.Resp.Request.URL.Query().Encode(),
			FileId:  ctx.UserData.(ContextUserData).ReqId,
			Url:     ctx.Resp.Request.URL.String(),
			Scheme:  ctx.Resp.Request.URL.Scheme,
			Host:    ctx.Resp.Request.Host,
			Method:  ctx.Resp.Request.Method,
			Time:    ctx.UserData.(ContextUserData).ElapsedTime,
			Headers: ctx.UserData.(ContextUserData).Header},
		Response: Response{
			Status:  ctx.Resp.StatusCode,
			Headers: ctx.Resp.Header,
			FileId:  fs.objectId},
		SocketUUID: ctx.Uuid.Bytes(),
		Date:       time.Now(),
	}
	return doc.Insert(content)
}

type TeeReadCloser struct {
	r io.Reader
	w io.WriteCloser
	c io.Closer
}

func NewTeeReadCloser(r io.ReadCloser, w io.WriteCloser) io.ReadCloser {
	return &TeeReadCloser{io.TeeReader(r, w), w, r}
}

func (t *TeeReadCloser) Read(b []byte) (int, error) {
	return t.r.Read(b)
}

func (t *TeeReadCloser) Close() error {
	err1 := t.c.Close()
	err2 := t.w.Close()
	if err1 == nil && err2 == nil {
		return nil
	}
	if err1 != nil {
		return err2
	}
	return err1
}

type FileStream struct {
	path        string
	db          mgo.Database
	contentType string
	objectId    bson.ObjectId
	f           *os.File
	ctx         *goproxy.ProxyCtx
}

func NewFileStream(path string, db mgo.Database, contentType string, objectId bson.ObjectId, ctx *goproxy.ProxyCtx) (*FileStream, error) {
	ctx.Logf("Creating file %s", objectId)
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &FileStream{path: path, db: db, contentType: contentType, objectId: objectId, f: f, ctx: ctx}, nil
}

func (fs *FileStream) Write(b []byte) (nr int, err error) {
	if fs.f == nil {
		fs.ctx.Logf("Trying to create again? %s", fs.objectId)
		fs.f, err = os.Create(fs.path)
		if err != nil {
			return 0, err
		}
	}
	return fs.f.Write(b)
}

func (fs *FileStream) Close() error {

	err := os.Remove(fs.path)

	if err != nil {
		fs.ctx.Warnf("Unable to delete file: %s", err)
	}

	fs.ctx.Logf("Closing file %s", fs.objectId)

	if fs.f == nil {
		return errors.New("FileStream was never written into")
	}

	fs.f.Seek(0, 0)

	err = saveFileToMongo(fs.db, fs.objectId, fs.contentType, fs.f, fs.objectId.Hex(), fs.ctx)

	if err != nil {
		fs.ctx.Warnf("Unable to save file to GridFS: %s", err)
		// Check if we need to save as documentation as well
	} else if fs.ctx.UserData != nil && fs.ctx.UserData.(ContextUserData).SaveAsDocumentation {
		err = saveAsDoc(fs)
		if err != nil {
			fs.ctx.Warnf("Unable to save as doc: %s", err)
		}
	}

	err = fs.f.Close()

	if err != nil {
		fs.ctx.Warnf("Failed to close file %s %s", fs.objectId, err.Error())
		return err
	}

	fs.ctx.Logf("File closed %s", fs.objectId)

	return err
}

func getMongoFileContent(ctx *goproxy.ProxyCtx, db mgo.Database, objId bson.ObjectId) (file *mgo.GridFile, err error) {
	ctx.Logf("db: %+v", db)
	file, err = db.GridFS("fs").OpenId(objId)

	if err != nil {
		return file, err
		if err == mgo.ErrNotFound {
		}
	}
	//defer file.Close()

	return file, err
}

// Store file in MongoDB GridFS
func saveFileToMongo(db mgo.Database, objId bson.ObjectId, contentType string, openFile io.Reader, fileName string, ctx *goproxy.ProxyCtx) error {
	ctx.Logf("db: %+v", db)
	mdbfile, err := db.GridFS("fs").Create(fileName)
	if err != nil {
		ctx.Warnf("Unable to create new GridFS file: %s", err)
		return err
	}
	defer func(mdbfile *mgo.GridFile, ctx *goproxy.ProxyCtx) {
		ctx.Logf("CLosing mdb file")
		err = mdbfile.Close()
		if err != nil {
			ctx.Warnf("Unable to close copy to mongo: %s", err)
			return
		}
		ctx.Logf("MongoDB file closed")
	}(mdbfile, ctx)

	mdbfile.SetContentType(contentType)
	mdbfile.SetId(objId)
	ctx.Logf("Copying to: %s", fileName)
	_, err = io.Copy(mdbfile, openFile)
	if err != nil {
		ctx.Warnf("Unable to copy to mongo: %s - %v", fileName, err)
		return err
	}
	ctx.Logf("Done copying")
	return nil
}

func getContentType(s string) string {
	arr := strings.Split(s, ";")
	return arr[0]
}
func ipAddrFromRemoteAddr(s string) string {
	idx := strings.LastIndex(s, ":")
	if idx == -1 {
		return s
	}
	return s[:idx]
}
