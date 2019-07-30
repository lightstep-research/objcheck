// Copyright 2017-2017 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not use this file except in compliance with the License. A copy of the License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific language governing permissions and limitations under the License.

package xrayport

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"strconv"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
)

const emptyHostRename = "empty_host_error"

// Client creates a shallow copy of the provided http client,
// defaulting to http.DefaultClient, with roundtripper wrapped
// with xray.RoundTripper.
func Client(c *http.Client) *http.Client {
	if c == nil {
		c = http.DefaultClient
	}
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{
		Transport:     RoundTripper(transport),
		CheckRedirect: c.CheckRedirect,
		Jar:           c.Jar,
		Timeout:       c.Timeout,
	}
}

// RoundTripper wraps the provided http roundtripper with xray.Capture,
// sets HTTP-specific xray fields, and adds the trace header to the outbound request.
func RoundTripper(rt http.RoundTripper) http.RoundTripper {
	return &roundtripper{rt}
}

type roundtripper struct {
	Base http.RoundTripper
}

// RoundTrip wraps a single HTTP transaction and add corresponding information into a subsegment.
func (rt *roundtripper) RoundTrip(r *http.Request) (*http.Response, error) {
	// var isEmptyHost bool
	var resp *http.Response
	host := r.Host
	if host == "" {
		if h := r.URL.Host; h != "" {
			host = h
		} else {
			host = emptyHostRename
			// isEmptyHost = true
		}
	}

	err := Capture(r.Context(), host, func(ctx context.Context) error {
		var err error
		span := opentracing.SpanFromContext(ctx)

		if span == nil {
			resp, err = rt.Base.RoundTrip(r)
			// logger.Warnf("failed to record HTTP transaction: span cannot be found.")
			return err
		}

		// seg := GetSegment(ctx)
		// if seg == nil {
		// 	resp, err = rt.Base.RoundTrip(r)
		// 	logger.Warnf("failed to record HTTP transaction: segment cannot be found.")
		// 	return err
		// }

		ct, e := NewClientTrace(ctx)
		if e != nil {
			return e
		}
		r = r.WithContext(httptrace.WithClientTrace(ctx, ct.httpTrace))

		// seg.Lock()

		// if isEmptyHost {
		// 	seg.Namespace = ""
		// } else {
		// 	seg.Namespace = "remote"
		// }

		ext.SpanKindRPCClient.Set(span)
		ext.HTTPMethod.Set(span, r.Method)
		ext.HTTPUrl.Set(span, r.URL.String())

		// seg.GetHTTP().GetRequest().Method = r.Method
		// seg.GetHTTP().GetRequest().URL = r.URL.String()

		_ = inject(opentracing.GlobalTracer(), span, r.Header)

		// r.Header.Set(TraceIDHeaderKey, seg.DownstreamHeader().String())
		// seg.Unlock()

		resp, err = rt.Base.RoundTrip(r)

		if resp != nil {
			ext.HTTPStatusCode.Set(span, uint16(resp.StatusCode)) // XXX Castin' like C
			contentLength, _ := strconv.Atoi(resp.Header.Get("Content-Length"))
			span.SetTag("ContentLength", contentLength)
			// seg.Lock()
			// seg.GetHTTP().GetResponse().Status = resp.StatusCode
			// seg.GetHTTP().GetResponse().ContentLength, _ = strconv.Atoi(resp.Header.Get("Content-Length"))

			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				// seg.Error = true
			}
			if resp.StatusCode == 429 {
				span.SetTag("Throttle", true)
				// seg.Throttle = true
			}
			if resp.StatusCode >= 500 && resp.StatusCode < 600 {
				span.SetTag("error", true)
				// seg.Fault = true
			}
			// seg.Unlock()
		}
		if err != nil {
			ct.spans.GotConn(nil, err) // XXX Why the nil check exists?
			// ct.subsegments.GotConn(nil, err)
		}

		return err
	})
	return resp, err
}

func inject(tracer opentracing.Tracer, span opentracing.Span, header http.Header) error {
	return tracer.Inject(span.Context(), opentracing.HTTPHeaders, opentracing.HTTPHeadersCarrier(header))
}
