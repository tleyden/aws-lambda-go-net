//
// Copyright 2017 Alsanium, SAS. or its affiliates. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package apigatewayproxy

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/ioutil"
	"log"
	gonet "net"
	"net/http"
	"net/url"
	"strings"

	"github.com/eawsy/aws-lambda-go-core/service/lambda/runtime"
	"github.com/eawsy/aws-lambda-go-event/service/lambda/runtime/event/apigatewayproxyevt"
	"github.com/eawsy/aws-lambda-go-net/service/lambda/runtime/net"
)

// Handler responds to a Lambda function invocation.
type Handler func(json.RawMessage, *runtime.Context) (*Response, error)

// Server defines parameters for handling requests coming from Amazon API
// Gateway. The zero value for Server is not a valid configuration, use New
// instead.
type Server struct {
	ln *net.LambdaListener
	ts map[string]bool
}

// New returns an initialized server to handle requests from Amazon API Gateway.
// The given listener must be an AWS Lambda network listener.
// The given media types slice may be nil, if Amazon API Gateway Binary support
// is not enabled. Otherwise, it should be an array of supported media types as
// configured in Amazon API Gateway.
func New(l gonet.Listener, ts []string) *Server {
	s := &Server{l.(*net.LambdaListener), make(map[string]bool)}
	for _, t := range ts {
		s.ts[t] = true
	}
	return s
}

// Response defines parameters for a well formed response AWS Lambda should
// return to Amazon API Gateway.
type Response struct {
	StatusCode      int               `json:"statusCode"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            string            `json:"body,omitempty"`
	IsBase64Encoded bool              `json:"isBase64Encoded"`
}

// Handle responds to a Lambda proxy function invocation from Amazon API
// Gateway.
// It transforms the Amazon API Gateway data format to a standard HTTP request
// format suitable for the Go net/http package. Then, it submits the data to the
// network listener so that it can be consumed by HTTP handler. Finally, it
// waits for the network listener to return response from handler and transmits
// it back to Amazon API Gateway.
func (s *Server) Handle(evt json.RawMessage, ctx *runtime.Context) (gwres *Response, dummy error) {
	gwreq := new(apigatewayproxyevt.Event)
	gwres = &Response{StatusCode: http.StatusInternalServerError}

	if err := json.Unmarshal(evt, &gwreq); err != nil {
		log.Println(err)
		return
	}

	var in, out bytes.Buffer

	addr := &gonet.TCPAddr{IP: gonet.ParseIP(gwreq.RequestContext.Identity.SourceIP)}

	u, err := url.Parse(gwreq.Path)
	if err != nil {
		log.Println(err)
		return
	}
	q := u.Query()
	for k, v := range gwreq.QueryStringParameters {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	dec := gwreq.Body
	if gwreq.IsBase64Encoded {
		data, err := base64.StdEncoding.DecodeString(dec)
		if err != nil {
			log.Println(err)
			return
		}
		dec = string(data)
	}

	req, err := http.NewRequest(gwreq.HTTPMethod, u.String(), strings.NewReader(dec))
	if err != nil {
		log.Println(err)
		return
	}

	gwreq.Body = "... truncated"

	for k, v := range gwreq.Headers {
		req.Header.Set(k, v)
	}
	hbody, err := json.Marshal(gwreq)
	if err != nil {
		log.Println(err)
		return
	}
	req.Header.Set("X-ApiGatewayProxy-Event", string(hbody))

	hctx, err := json.Marshal(ctx)
	if err != nil {
		log.Println(err)
		return
	}
	req.Header.Set("X-ApiGatewayProxy-Context", string(hctx))

	req.Host = gwreq.Headers["Host"]

	req.Write(&in)

	s.ln.Handle(addr, &in, &out)

	res, err := http.ReadResponse(bufio.NewReader(&out), req)
	if err != nil {
		log.Println(err)
		return
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Println(err)
		return
	}
	res.Body.Close()

	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(body)
		res.Header.Set("Content-Type", ct)
	}

	if _, ok := s.ts[ct]; ok {
		gwres.Body = base64.StdEncoding.EncodeToString(body)
		gwres.IsBase64Encoded = true
	} else {
		gwres.Body = string(body)
	}

	gwres.Headers = make(map[string]string)
	for k := range res.Header {
		gwres.Headers[k] = res.Header.Get(k)
	}

	gwres.StatusCode = res.StatusCode

	return
}
