package main

import (
	"flag"
	"labix.org/v2/mgo"
	"labix.org/v2/mgo/bson"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
	//"github.com/elazarl/goproxy/ext/html"
)

type ContextUserData struct {
	Store bool
	Time  int64
}

type Content struct {
	//Id bson.ObjectId
	Request  Request  "request"
	Response Response "response"
	Date    time.Time   "date"
}

type Request struct {
	Body    string      "body"
	//Date    time.Time   "date"
	Host    string      "host"
	Method  string      "method"
	Path    string      "path"
	Time    float32     "time"
	Headers http.Header "headers"
}

type Response struct {
	Body    string      "body"
	Headers http.Header "headers"
	Status  int         "status"
}

func main() {
	verbose := flag.Bool("v", false, "should every proxy request be logged to stdout")
	mongourl := flag.String("mongourl", "", "record request/response in mongodb")
	mock := flag.Bool("m", false, "send fake responses")
	addr := flag.String("l", ":8080", "on which address should the proxy listen")

	flag.Parse()

	c := &mgo.Collection{}

	if len(*mongourl) != 0 {
		// Mongo DB connection
		session, err := mgo.Dial(*mongourl)
		if err != nil {
			panic(err)
		}
		defer session.Close()

		// Optional. Switch the session to a monotonic behavior.
		session.SetMode(mgo.Monotonic, true)

		c = session.DB("proxyservice").C("log_logentry")
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = *verbose

	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		log.Printf("Request: %s %s %s", req.Method, req.Host, req.RequestURI)

		host := req.Host
		// Request to domain--name-co-uk.mocky.dev
		// will be forwarded to domain-name.co.uk
		if strings.Contains(host, ".mocky.dev") {
			host = strings.Replace(host, ".mocky.dev", "", 1)
			host = strings.Replace(host, "-", ".", -1)
			host = strings.Replace(host, "..", "-", -1)
			log.Printf("Target Host: %s", host)
			req.Host = host

		}

		if c != nil && *mock && req.Method != "CONNECT" {
			result := Content{}
			ctx.Logf("Looking for existing request")
			/*fmt.Println("RequestURI:", req.RequestURI)
			  fmt.Println("Path:", req.URL.Path)
			  fmt.Println("Host:", req.Host)
			  fmt.Println("Method:", req.Method)*/
			err := c.Find(bson.M{"request.host": req.Host, "request.method": req.Method, "response.status": 200, "request.path": req.URL.Path}).Sort("-date").One(&result)
			if err == nil {
				ctx.Logf("Found one")
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

				resp := goproxy.NewResponse(req, goproxy.ContentTypeHtml, result.Response.Status, result.Response.Body)
				ctx.UserData = ContextUserData{Store: false, Time: 0}
				return req, resp
			}

		}

		ctx.UserData = ContextUserData{Store: true, Time: time.Now().UnixNano()}
		return req, nil
	})

	proxy.OnResponse().Do(goproxy.HandleBytes(
		func(b []byte, ctx *goproxy.ProxyCtx) []byte {
			ctx.Logf("Method: %s - host: %s", ctx.Resp.Request.Method, ctx.Resp.Request.Host)
			if c != nil && ctx.UserData != nil && ctx.UserData.(ContextUserData).Store && ctx.Resp.StatusCode == 200 && ctx.Resp.Request.Method != "CONNECT" {
				// Get Body as string
				s := string(b[:])
				ctx.Logf("Recording both request/response")
				content := Content{
					//Id: bson.NewObjectId(),
					Request:  Request{Path: ctx.Resp.Request.URL.Path, Host: ctx.Resp.Request.Host, Method: ctx.Resp.Request.Method, Time: float32(time.Now().UnixNano() - ctx.UserData.(ContextUserData).Time) / 1.0e9, Headers: ctx.Resp.Request.Header},
					Response: Response{Status: ctx.Resp.StatusCode, Headers: ctx.Resp.Header, Body: s},
                    Date: time.Now(), 
                    }

				err := c.Insert(content)
				if err != nil {
					ctx.Logf("Can't insert document: %v\n", err)
				}
				//ctx.Logf("Body: %s", s)
			}

			return b
		}))

	log.Println("Starting Proxy")
	log.Fatalln(http.ListenAndServe(*addr, proxy))
}
