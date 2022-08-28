/*
from https://github.com/levigross/grequests
*/
package iharbor

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"os"
)

// Response is what is returned to a user when they fire off a request
type Response struct {

	// This is the Go error flag – if something went wrong within the request, this flag will be set.
	Error error

	// We want to abstract (at least at the moment) the Go http.Response object away. So we are going to make use of it
	// internal but not give the user access
	RawResponse *http.Response

	// StatusCode is the HTTP Status Code returned by the HTTP Response. Taken from resp.StatusCode
	StatusCode int

	// Header is a net/http/Header structure
	Header http.Header

	responseByteBuffer *bytes.Buffer
}

func buildResponse(resp *http.Response) *Response {

	return &Response{
		Error:              nil,
		RawResponse:        resp,
		StatusCode:         resp.StatusCode,
		Header:             resp.Header,
		responseByteBuffer: bytes.NewBuffer([]byte{}),
	}
}

// Read is part of our ability to support io.ReadCloser if someone wants to make use of the raw body
func (r *Response) Read(p []byte) (n int, err error) {

	if r.Error != nil {
		return -1, r.Error
	}

	return r.RawResponse.Body.Read(p)
}

// Close is part of our ability to support io.ReadCloser if someone wants to make use of the raw body
func (r *Response) Close() error {

	if r.Error != nil {
		return r.Error
	}

	_, err := io.Copy(ioutil.Discard, r)
	if err != nil {
		return err
	}

	return r.RawResponse.Body.Close()
}

// DownloadToFile allows you to download the contents of the response to a file
func (r *Response) DownloadToFile(fileName string) error {

	if r.Error != nil {
		return r.Error
	}

	fd, err := os.Create(fileName)

	if err != nil {
		return err
	}

	defer r.Close() // This is a noop if we use the internal ByteBuffer
	defer fd.Close()

	if _, err := io.Copy(fd, r.getInternalReader()); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// getInternalReader because we implement io.ReadCloser and optionally hold a large buffer of the response (created by
// the user's request)
func (r *Response) getInternalReader() io.Reader {

	if r.responseByteBuffer.Len() != 0 {
		return r.responseByteBuffer
	}
	return r
}

// JSON is a method that will populate a struct that is provided `userStruct` with the JSON returned within the
// response body
func (r *Response) JSON(userStruct interface{}) error {

	if r.Error != nil {
		return r.Error
	}

	jsonDecoder := json.NewDecoder(r.getInternalReader())
	defer r.Close()

	return jsonDecoder.Decode(&userStruct)
}

// createResponseBytesBuffer is a utility method that will populate the internal byte reader – this is largely used for .String()
// and .Bytes()
func (r *Response) populateResponseByteBuffer() {

	// Have I done this already?
	if r.responseByteBuffer.Len() != 0 {
		return
	}

	defer r.Close()

	// Is there any content?
	if r.RawResponse.ContentLength == 0 {
		return
	}

	// Did the server tell us how big the response is going to be?
	if r.RawResponse.ContentLength > 0 {
		r.responseByteBuffer.Grow(int(r.RawResponse.ContentLength))
	}

	if _, err := io.Copy(r.responseByteBuffer, r); err != nil && err != io.EOF {
		r.Error = err
		r.RawResponse.Body.Close()
	}

}

// Bytes returns the response as a byte array
func (r *Response) Bytes() []byte {

	if r.Error != nil {
		return nil
	}

	r.populateResponseByteBuffer()

	// Are we still empty?
	if r.responseByteBuffer.Len() == 0 {
		return nil
	}
	return r.responseByteBuffer.Bytes()

}

// String returns the response as a string
func (r *Response) String() string {
	if r.Error != nil {
		return ""
	}

	r.populateResponseByteBuffer()

	return r.responseByteBuffer.String()
}

// ClearResponseBuffer is a function that will clear the internal buffer that we use to hold the .String() and .Bytes()
// data. Once you have used these functions – you may want to free up the memory.
func (r *Response) ClearResponseBuffer() {

	if r == nil || r.responseByteBuffer == nil {
		return
	}

	r.responseByteBuffer.Reset()
}
