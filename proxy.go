package main

import (
	"flag"
	"labix.org/v2/mgo"
	"labix.org/v2/mgo/bson"
	"log"
	"net/http"
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
	//Body   io.Reader
	Body   []byte
	Header http.Header
}

type Content struct {
	//Id       bson.ObjectId
	Request  Request   "request"
	Response Response  "response"
	Date     time.Time "date"
}

type Request struct {
	Origin	string "origin"
	Body   string "body"
	FileId bson.ObjectId
	Query  string "query"
	//Date    time.Time   "date"
	Host    string      "host"
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

func NewResponse(r *http.Request, contentType string, status int, body []byte) *http.Response {
	resp := &http.Response{}
	resp.Request = r
	resp.TransferEncoding = r.TransferEncoding
	resp.Header = make(http.Header)
	resp.Header.Add("Content-Type", contentType)
	resp.StatusCode = status
	buf := bytes.NewBuffer(body)
	resp.ContentLength = int64(buf.Len())
	resp.Body = ioutil.NopCloser(buf)
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
		c = db.C("log_logentry")
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = *verbose

	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {

		//log.Printf("Request: %s %s %s", req.Method, req.Host, req.RequestURI)

		host := req.Host
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
		req.Host = host

		//log.Printf("%+v", req)

		var reqbody []byte

		if c.Database != nil && *mock && req.Method != "CONNECT" {
			//reqbody := string(body[:])
			//log.Printf("request body: %s", reqbody)
			result := Content{}
			//ctx.Logf("Looking for existing request")
			/*fmt.Println("RequestURI:", req.RequestURI)
			  fmt.Println("Path:", req.URL.Path)
			  fmt.Println("Host:", req.Host)
			  fmt.Println("Method:", req.Method)*/
			err := c.Find(bson.M{"request.host": req.Host, "request.method": req.Method, "response.status": 200, "request.path": req.URL.Path}).Sort("-date").One(&result)
			if err == nil {
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

			// read the whole body
			reqbody, err = ioutil.ReadAll(req.Body)
			if err != nil {
				ctx.Warnf("Cannot read request body %s", err)
			}

			defer req.Body.Close()
			req.Body = ioutil.NopCloser(bytes.NewBuffer(reqbody))

		}

		ctx.UserData = ContextUserData{Store: true, Time: time.Now().UnixNano(), Body: reqbody, Header: req.Header}
		return req, nil
	})

	proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		//ctx.Logf("Method: %s - host: %s", ctx.Resp.Request.Method, ctx.Resp.Request.Host)
		if c.Database != nil && ctx.UserData != nil && ctx.UserData.(ContextUserData).Store && ctx.Resp.Request.Method != "CONNECT" {
			// get response content type
			respctype := getContentType(ctx.Resp.Header.Get("Content-Type"))

			//log.Printf("Resp Contenttype %s", respctype)

			respid := bson.NewObjectId()
			//log.Printf("Resp id: %s, host: %s", respid.Hex(), ctx.Resp.Request.Host)

			filename := filepath.Join(tmpdir, respid.Hex())

			//log.Printf("Duplicating Body file id: %s", respid.String())
			resp.Body = NewTeeReadCloser(resp.Body, NewFileStream(filename, *db, respctype, respid))

			reqctype := getContentType(ctx.Resp.Request.Header.Get("Content-Type"))

			//log.Printf("Req Contenttype %s", reqctype)

			if reqctype == "application/x-www-form-urlencoded" {
				//log.Printf("setting req content type to text/plain for saving to mongo")
				reqctype = "text/plain"
			}

			bodyreader := bytes.NewReader(ctx.UserData.(ContextUserData).Body)

			reqid := bson.NewObjectId()

			saveFileToMongo(*db, reqid, reqctype, bodyreader, reqid.Hex())

			// prepare document
			content := Content{
				//Id: docid,
				Request: Request{
					Origin:	 ipAddrFromRemoteAddr(ctx.Resp.Request.RemoteAddr),
					Path:    ctx.Resp.Request.URL.Path,
					Query:   ctx.Resp.Request.URL.Query().Encode(),
					FileId:  reqid,
					Host:    ctx.Resp.Request.Host,
					Method:  ctx.Resp.Request.Method,
					Time:    float32(time.Now().UnixNano()-ctx.UserData.(ContextUserData).Time) / 1.0e9,
					Headers: ctx.UserData.(ContextUserData).Header},
				Response: Response{
					Status:  ctx.Resp.StatusCode,
					Headers: ctx.Resp.Header,
					FileId:  respid},
				Date: time.Now(),
			}

			err := c.Insert(content)
			if err != nil {
				ctx.Logf("Can't insert document: %v\n", err)
			}

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
}

func NewFileStream(path string, db mgo.Database, contentType string, objectId bson.ObjectId) *FileStream {
	return &FileStream{path: path, db: db, contentType: contentType, objectId: objectId, f: nil}
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
	saveFileToMongo(fs.db, fs.objectId, fs.contentType, fs.f, fs.objectId.Hex())
	err := fs.f.Close()
	if err == nil {
		err2 := os.Remove(fs.path)
		if err2 != nil {
			log.Printf("Unable to delete file")
		}
	}
	return err
}

// Store file in MongoDB GridFS
func saveFileToMongo(db mgo.Database, objId bson.ObjectId, contentType string, openFile io.Reader, fileName string) {
	mdbfile, err := db.GridFS("fs").Create(fileName)
	if err == nil {
		mdbfile.SetContentType(contentType)
		mdbfile.SetId(objId)
		_, err = io.Copy(mdbfile, openFile)
		if err != nil {
			log.Printf("Unable to copy to mongo")
		}
		err = mdbfile.Close()
		if err != nil {
			log.Printf("Unable to close copy to mongo")
		}
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
