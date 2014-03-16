package main

import (
	"flag"
	"labix.org/v2/mgo"
	"labix.org/v2/mgo/bson"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/elazarl/goproxy/ext/html"
)

type ContextUserData struct {
	Store bool
	Time  int64
}

type Content struct {
	//Id bson.ObjectId
	Request  Request  "request"
	Response Response "response"
}

type Request struct {
	Body    string      "body"
	Date    time.Time   "date"
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

type stoppableListener struct {
	net.Listener
	sync.WaitGroup
}

type stoppableConn struct {
	net.Conn
	wg *sync.WaitGroup
}

func newStoppableListener(l net.Listener) *stoppableListener {
	return &stoppableListener{l, sync.WaitGroup{}}
}

func (sl *stoppableListener) Accept() (net.Conn, error) {
	c, err := sl.Listener.Accept()
	if err != nil {
		return c, err
	}
	sl.Add(1)
	return &stoppableConn{c, &sl.WaitGroup}, nil
}

func (sc *stoppableConn) Close() error {
	sc.wg.Done()
	return sc.Conn.Close()
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

		c = session.DB("proxyservice").C("log")
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = *verbose

	proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		ctx.Logf("received connect for", host)
		return goproxy.MitmConnect, host
	})

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
			err := c.Find(bson.M{"request.host": req.Host, "request.method": req.Method, "response.status": 200, "request.path": req.URL.Path}).Sort("-request.date").One(&result)
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

	proxy.OnResponse().Do(goproxy_html.HandleString(
		func(s string, ctx *goproxy.ProxyCtx) string {
			if c != nil && ctx.UserData != nil && ctx.UserData.(ContextUserData).Store && ctx.Req.Method != "CONNECT" && ctx.Resp.StatusCode == 200 {
				ctx.Logf("We should probably save this response")
				content := Content{
					//Id: bson.NewObjectId(),
					Request:  Request{Path: ctx.Req.URL.Path, Host: ctx.Req.Host, Method: ctx.Req.Method, Date: time.Now(), Time: float32(ctx.UserData.(ContextUserData).Time) / 1.0e9, Headers: ctx.Req.Header},
					Response: Response{Status: ctx.Resp.StatusCode, Headers: ctx.Resp.Header, Body: s}}

				err := c.Insert(content)
				if err != nil {
					ctx.Logf("Can't insert document: %v\n", err)
				}
			}

			ctx.Logf(s)
			return s
		}))

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal("listen:", err)
	}
	sl := newStoppableListener(l)
	ch := make(chan os.Signal)
	signal.Notify(ch, os.Interrupt)
	go func() {
		<-ch
		log.Println("Exitting")
		sl.Add(1)
		sl.Close()
		sl.Done()
	}()
	signal.Notify(ch, os.Interrupt)

	log.Println("Starting Proxy")
	log.Fatalln(http.Serve(sl, proxy))
	sl.Wait()

}
