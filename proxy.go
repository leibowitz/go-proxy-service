package main

import (
	"flag"
	"labix.org/v2/mgo"
	"labix.org/v2/mgo/bson"
	"log"
	"net/http"
	"strconv"
	//"fmt"
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
	"github.com/twinj/uuid"
)

type ContextUserData struct {
	Store bool
	Time  int64
	Body  io.Reader
	//Body   []byte
	Header      http.Header
	Origin      string
	Collections []*mgo.Collection
}

type Content struct {
	Id         bson.ObjectId
	Request    Request   "request"
	Response   Response  "response"
	Date       time.Time "date"
	SocketUUID []byte    "uuid"
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
	cache := new(mgo.Collection)
	h := new(mgo.Collection)
	rules := new(mgo.Collection)

	if len(*mongourl) != 0 {
		// Mongo DB connection
		session, err := mgo.Dial(*mongourl)
		if err != nil {
			panic(err)
		}
		defer session.Close()

		// Optional. Switch the session to a monotonic behavior.
		session.SetMode(mgo.Monotonic, true)

		db = session.DB("proxyservice")
		c = db.C("log_logentry")     // capped collection
		cache = db.C("log_logcache") // forever cache
		h = db.C("log_hostrewrite")
		rules = db.C("log_rules")
	} else {
		db = nil
		c = nil
		cache = nil
		h = nil
		rules = nil
	}

	uuid.SwitchFormat(uuid.CleanHyphen, false)
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = *verbose

	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		origin := ipAddrFromRemoteAddr(ctx.Req.RemoteAddr)
		ctx.Logf("Origin: %s", origin)
		/*ctx.RoundTripper = goproxy.RoundTripperFunc(func (req *http.Request, ctx *goproxy.ProxyCtx) (resp *http.Response, err error) {
			//data := transport.RoundTripDetails{}
			data, resp, err := tr.DetailedRoundTrip(req)
			//log.Printf("%+v", data)
			return
		})*/

		rewrite := Rewrite{}
		if h != nil && h.Database != nil {
			err := h.Find(bson.M{"host": req.Host, "active": true}).One(&rewrite)
			if err == nil {
				req.URL.Scheme = rewrite.DProtocol
				req.URL.Host = rewrite.DHost
				req.Host = rewrite.DHost
				ctx.Logf("Rewrite: %+v, URL: %+v", rewrite, req.URL)
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

		// Use the log_logentry for storing this request by default
		collections := make([]*mgo.Collection, 0)
		collections = append(collections, c)

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
			ctx.Logf("Looking for a rule: %+v", b)
			err := rules.Find(b).Sort("dynamic").One(&rule)
			//log.Printf("Query: %+v, Res: %+v", b, rule)
			if err == nil {
				ctx.Logf("Found rule: %+v", rule)
				status, err := strconv.Atoi(rule.Status)
				var cachedb *mgo.Collection
				if cache != nil && cache.Database != nil {
					cachedb = cache // use the logcache collection (forever cache)
				} else if c != nil && c.Database != nil {
					cachedb = c // use the logentry collection (capped collection)
				}

				if rule.Dynamic && cachedb != nil && cachedb.Database != nil {
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
					err = cachedb.Find(reqQuery).Sort("-date").One(&result)
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
							ctx.UserData = ContextUserData{Store: true, Time: 0, Body: req.Body, Header: req.Header, Origin: origin, Collections: collections}
							return req, resp
						} else {
							ctx.Logf("Couldn't retrieve the response body: %+v", err)
						}
					} else {
						ctx.Logf("Couldn't find a dynamic response matching: %+v", err)
						// We need to store this request for the future
						collections = append(collections, cache)
					}
				} else {
					ctx.Logf("Found a static rule matching, returning it: %+v", rule)
					reqbody := ioutil.NopCloser(bytes.NewBufferString(rule.ReqBody))
					respbody := ioutil.NopCloser(bytes.NewBufferString(rule.Body))
					resp := NewResponse(req, rule.RespHeader, status, respbody)
					ctx.Delay = rule.Delay
					ctx.UserData = ContextUserData{Store: true, Time: 0, Body: reqbody, Header: req.Header, Origin: origin, Collections: collections}
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

		ctx.UserData = ContextUserData{Store: true, Time: time.Now().UnixNano(), Body: bodyreader, Header: req.Header, Origin: origin, Collections: collections}
		return req, nil
	})

	proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		//ctx.Logf("Method: %s - host: %s", ctx.Resp.Request.Method, ctx.Resp.Request.Host)
		if c != nil && c.Database != nil && ctx.UserData != nil && ctx.UserData.(ContextUserData).Store && ctx.Resp.Request.Method != "CONNECT" && db != nil && ctx.UserData.(ContextUserData).Collections != nil {
			// get response content type
			respctype := getContentType(ctx.Resp.Header.Get("Content-Type"))

			//log.Printf("Resp Contenttype %s", respctype)

			respid := bson.NewObjectId()
			//log.Printf("Resp id: %s, host: %s", respid.Hex(), ctx.Resp.Request.Host)

			filename := filepath.Join(tmpdir, respid.Hex())

			//log.Printf("Duplicating Body file id: %s", respid.String())
			fs := NewFileStream(filename, *db, respctype, respid, ctx)

			reqctype := getContentType(ctx.Resp.Request.Header.Get("Content-Type"))

			//log.Printf("Req Contenttype %s", reqctype)

			if reqctype == "application/x-www-form-urlencoded" {
				//log.Printf("setting req content type to text/plain for saving to mongo")
				reqctype = "text/plain"
			}

			reqid := bson.NewObjectId()
			//log.Printf("Req id: %s, host: %s", reqid.Hex(), ctx.Resp.Request.Host)

			saveFileToMongo(*db, reqid, reqctype, ctx.UserData.(ContextUserData).Body, reqid.Hex(), ctx)

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
					Time:    float32(time.Now().UnixNano()-ctx.UserData.(ContextUserData).Time) / 1.0e9,
					Headers: ctx.UserData.(ContextUserData).Header},
				Response: Response{
					Status:  ctx.Resp.StatusCode,
					Headers: ctx.Resp.Header,
					FileId:  respid},
				SocketUUID: ctx.Uuid.Bytes(),
				Date:       time.Now(),
			}

			id := bson.NewObjectId()
			content.Id = id
			for _, collection := range ctx.UserData.(ContextUserData).Collections {
				ctx.Logf("trying to insert with an id")
				err := collection.Insert(content)
				if err != nil {
					ctx.Logf("Can't insert document into %s: %v", collection.Name, err)
				} else {
					ctx.Logf("MongoDB document saved to %s: %+v", collection.Name, content)
				}
			}

			resp.Body = NewTeeReadCloser(resp.Body, fs)
		}
		return resp
	})

	log.Println("Starting Proxy")

	log.Fatalln(http.ListenAndServe(*addr, proxy))
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

func NewFileStream(path string, db mgo.Database, contentType string, objectId bson.ObjectId, ctx *goproxy.ProxyCtx) *FileStream {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	return &FileStream{path: path, db: db, contentType: contentType, objectId: objectId, f: f, ctx: ctx}
}

func (fs *FileStream) Write(b []byte) (nr int, err error) {
	if fs.f == nil {
		fs.f, err = os.Create(fs.path)
		if err != nil {
			return 0, err
		}
	}
	return fs.f.Write(b)
}

func (fs *FileStream) Close() error {
	if fs.f == nil {
		return errors.New("FileStream was never written into")
	}
	fs.f.Seek(0, 0)
	saveFileToMongo(fs.db, fs.objectId, fs.contentType, fs.f, fs.objectId.Hex(), fs.ctx)
	err := fs.f.Close()
	if err == nil {
		err2 := os.Remove(fs.path)
		if err2 != nil {
			fs.ctx.Logf("Unable to delete file")
		}
	}
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
func saveFileToMongo(db mgo.Database, objId bson.ObjectId, contentType string, openFile io.Reader, fileName string, ctx *goproxy.ProxyCtx) {
	ctx.Logf("db: %+v", db)
	mdbfile, err := db.GridFS("fs").Create(fileName)
	if err == nil {
		mdbfile.SetContentType(contentType)
		mdbfile.SetId(objId)
		ctx.Logf("Copying to: %s", fileName)
		_, err = io.Copy(mdbfile, openFile)
		if err != nil {
			ctx.Logf("Unable to copy to mongo: %s - %v", fileName, err)
		}
		ctx.Logf("Done copying, closing")
		err = mdbfile.Close()
		if err != nil {
			ctx.Logf("Unable to close copy to mongo")
		}
		ctx.Logf("MongoDB body file saved")
	}
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
