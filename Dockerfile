# Start from a Debian image with the latest version of Go installed
# and a workspace (GOPATH) configured at /go.
FROM golang

# Copy the local package files to the container's workspace.
ADD . /go/src/github.com/leibowitz/go-proxy-service

RUN go get github.com/tools/godep

# Build the command inside the container.
# (You may fetch or manage dependencies here,
# either manually or with a tool like "godep".)
RUN go get -d github.com/leibowitz/go-proxy-service

RUN cd /go/src/github.com/leibowitz/go-proxy-service && godep get && go install

# Run the outyet command by default when the container starts.
CMD /go/bin/go-proxy-service

# Document that the service listens on port 8080.
EXPOSE 8080
