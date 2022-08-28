package iharbor

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const ErrCodeNoSuchKey string = "NoSuchKey"
const ErrCodeNoParentPath string = "NoParentPath"

// Error IHarborError
type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (e Error) Error() string {
	return "code:" + e.Code + "," + e.Message
}

func (e *Error) SetMessage(s string) {
	e.Message = s
}

func NewIHarborError(code, message string, statusCode int) Error {
	return Error{
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
	}
}
func wrapIHarborError(r *Response, message string) Error {
	data := r.Bytes()
	errCode := "Error"
	code, ok, err := GetValueFromJSON("code", string(data))
	if err == nil && ok {
		code, ok := code.(string)
		if ok {
			errCode = code
		}
	}
	text := parseResponseMsg(data)
	msg := message
	if text != "" {
		msg += "," + text
	}
	return NewIHarborError(errCode, msg, r.StatusCode)
}

type Client struct {
	Https         bool
	Endpoint      string
	Authorization string
}

func NewIHarborClient(https bool, endpoint string, token string) (*Client, error) {
	if strings.HasPrefix(endpoint, "http") {
		return nil, errors.New("“endpoint”需要是域名，不能包含‘http’")
	}
	c := Client{
		Https:         https,
		Endpoint:      endpoint,
		Authorization: "Token " + token,
	}
	return &c, nil
}

func buildPath(slice []string) string {
	var a []string
	for _, value := range slice {
		if v := strings.Trim(value, "/"); v != "" {
			a = append(a, v)
		}
	}
	return strings.Join(a, "/")
}

// CutPathAndName 切割一个路径，return 父路经和文件目录名称
func CutPathAndName(pathName string) (string, string) {

	i := strings.LastIndex(pathName, "/")
	if i >= 0 {
		return pathName[:i], pathName[i+1:]
	}
	return "", pathName
}

// JSON2map json转换为map
func JSON2map(data []byte) (map[string]interface{}, error) {

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetValueFromJSON 从json中获取指定键的值
func GetValueFromJSON(key, json string) (value interface{}, ok bool, err error) {
	mapData, e := JSON2map([]byte(json))
	if e != nil {
		err = e
		ok = false
		value = nil
		return
	}
	value, ok = mapData[key]
	err = nil
	return
}

func parseResponseMsg(respBody []byte) string {

	data := respBody
	mapData, err := JSON2map(data)
	if err != nil {
		return string(data)
	}

	keys := [...]string{"code_text", "detail", "non_field_errors"}
	ret := ""
	for _, k := range keys {
		val, ok := mapData[k]
		if ok {
			var msg interface{}

			switch val.(type) {
			case string:
				msg = val
			case []interface{}:
				msg = val.([]interface{})[0].(interface{})
			default:
				msg = val
			}

			text, err := json.Marshal(msg)
			if err == nil {
				ret = strings.Trim(string(text), "\"")
			}

			break
		}
	}
	return ret
}

// TryToGetSize tries to get upfront size from reader.
func TryToGetSize(r io.Reader) (int64, error) {
	switch f := r.(type) {
	case *os.File:
		fileInfo, err := f.Stat()
		if err != nil {
			return 0, errors.New("os.File.Stat() error")
		}
		return fileInfo.Size(), nil
	case *bytes.Buffer:
		return int64(f.Len()), nil
	case *bytes.Reader:
		// Returns length of unread data only.
		return int64(f.Len()), nil
	case *strings.Reader:
		return f.Size(), nil
	}
	return 0, errors.New("unsupported type of io.Reader:" + fmt.Sprintf("%T", r))
}

func MD5sum(r io.Reader) string {
	nr := bufio.NewReader(r)
	h := md5.New()

	_, err := io.Copy(h, nr)
	if err != nil {
		return ""
	}

	return fmt.Sprintf("%x", h.Sum(nil))

}

// buildURL return url.URL结构体对象
// If you do not intend to use the `params` you can just pass nil
func (c *Client) buildURL(urlPath string, params map[string]string) url.URL {

	query := ""
	if params != nil {
		v := url.Values{}
		for key, value := range params {
			v.Add(key, value)
		}
		query = v.Encode() //编码后的query string
	}
	scheme := "http"
	if c.Https {
		scheme = "https"
	}
	u := url.URL{
		Scheme:   scheme,
		Host:     c.Endpoint,
		Path:     urlPath, //未编码的path
		RawQuery: query,
	}

	return u
}

func (c *Client) SetAuth(auth string) {
	c.Authorization = auth
}

func (c *Client) setAuthHeader(request *http.Request) {
	request.Header.Set("Authorization", c.Authorization)
}

// CreateDir : create a new dir
func (c *Client) CreateDir(bucketName string, dirPath string) error {

	path := buildPath([]string{"api/v1/dir", bucketName, dirPath})
	path = path + "/"
	url := c.buildURL(path, nil)
	urlStr := url.String()
	request, err := http.NewRequest("POST", urlStr, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(request)
	//request.Header.Set("Authorization", c.Authorization)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	response := buildResponse(resp)
	if response.StatusCode == 201 {
		return nil
	}
	if response.StatusCode == 409 {
		s := response.String()
		existing, ok, err := GetValueFromJSON("code", s)
		if err == nil && ok {
			if r, ok := existing.(string); ok && (r == "KeyAlreadyExists") {
				return nil
			}
		}
	}
	return wrapIHarborError(response, "Create dir failed,"+urlStr)
}

// CreatePath CreateDirPath : create a dir path
func (c *Client) CreatePath(bucketName string, dirPath string) error {

	dirPath = strings.Trim(dirPath, "/")
	dirs := strings.Split(dirPath, "/")
	parendPath := ""
	for _, d := range dirs {
		path := strings.Join([]string{parendPath, d}, "/")
		if err := c.CreateDir(bucketName, path); err != nil {
			return err
		}

		parendPath = path
	}
	return nil
}

func (c *Client) uploadOneChunkRequest(uri string, body io.Reader, headers map[string]string) (*http.Response, error) {

	request, err := http.NewRequest("POST", uri, body)
	if err != nil {
		return nil, err
	}
	c.setAuthHeader(request)

	for key, value := range headers {
		request.Header.Add(key, value)
	}

	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// make path if err is ParentPathError
// return:
//		nil     is ParentPathError, and make it ok
//		error   is ParentPathError, and make it failed;or is not ParentPathError
func (c *Client) makeIfParentPathError(err error, bucketName, name string) error {
	if c.IsNoParentPathErr(err) {
		dirs := strings.Split(strings.TrimSuffix(name, "/"), "/")
		dirPath := strings.Join(dirs[:len(dirs)-1], "/")
		if dirPath == "" {
			return NewIHarborError(ErrCodeNoParentPath,
				"Is NoParentPath Error, "+err.Error()+";but invalid dir path:"+name, 500)
		}

		if e := c.CreatePath(bucketName, dirPath); e != nil {
			return NewIHarborError(ErrCodeNoParentPath,
				"Is NoParentPath Error,"+err.Error()+";Then CreatePath failed,"+e.Error(), 500)

		}
		return nil
	}

	return NewIHarborError("Error",
		"Is not NoParentPath Error,"+err.Error(), 500)
}

// UploadOneChunk 上传一个对象数据块
// param bucketName: 桶名称
// param objPathName: 桶下全路径对象名称
// param offset: 数据块在对象中的字节偏移量
// param chunkSize: 数据块字节长度
// param r: 数据块
// param isReset: true(如果对象已存在重置对象大小为0)
func (c *Client) UploadOneChunk(bucketName, objPathName string, offset int64, chunkSize int64, r io.Reader, isReset bool) error {

	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)
	writer, err := bodyWriter.CreateFormFile("chunk", "chunk")
	if err != nil {
		return err
	}
	ncloser := ioutil.NopCloser(&io.LimitedReader{R: r, N: chunkSize})
	_, err = io.Copy(writer, ncloser)
	if err != nil {
		return err
	}

	params := map[string]string{
		"chunk_size":   strconv.FormatInt(chunkSize, 10),
		"chunk_offset": strconv.FormatInt(offset, 10),
	}
	for key, val := range params {
		_ = bodyWriter.WriteField(key, val)
	}
	if e := bodyWriter.Close(); e != nil {
		return e
	}

	path := buildPath([]string{"api/v1/obj", bucketName, objPathName})
	path = path + "/"
	querys := map[string]string{"reset": "true"}
	if !isReset {
		querys = nil
	}
	url := c.buildURL(path, querys)
	urlStr := url.String()

	headers := map[string]string{
		"Content-Type": bodyWriter.FormDataContentType(),
	}

	bodySlice := bodyBuf.Bytes()
	body := bytes.NewReader(bodySlice)
	resp, err := c.uploadOneChunkRequest(urlStr, body, headers)
	if err != nil {
		return err
	}
	if resp.StatusCode == 200 {
		resp.Body.Close()
		return nil
	}

	defer resp.Body.Close()
	response := buildResponse(resp)
	ierr := wrapIHarborError(response, "UploadOneChunk failed,"+urlStr)

	if err := c.makeIfParentPathError(ierr, bucketName, objPathName); err != nil {
		return err
	}

	// try once again
	body.Seek(0, io.SeekStart)
	resp2, err := c.uploadOneChunkRequest(urlStr, body, headers)
	if err != nil {
		return err
	}
	if resp2.StatusCode == 200 {
		resp2.Body.Close()
		return nil
	}

	defer resp2.Body.Close()
	response2 := buildResponse(resp2)
	return wrapIHarborError(response2, "UploadOneChunk failed,"+urlStr)
}

// MultipartUploadObject 上传一个对象
// param bucketName: 桶名称
// param objPathName: 桶下全路径对象名称
// param fileName: 要上传的文件路径
// param startOffset: 从文件的此偏移量处开始上传
func (c *Client) MultipartUploadObject(bucketName string, objPathName string, r io.Reader, partSizeMB int) error {

	var readSize = 1024 * 1024 * int64(partSizeMB)
	objectSize, err := TryToGetSize(r)
	if err != nil {
		return errors.New("try to get size error")
	}

	chunksnum := int(objectSize / readSize) //int(math.Floor(float64(objectSize) / readSize))
	lastslice := objectSize % readSize
	ncloser := ioutil.NopCloser(r)
	var offset int64
	for chunk := 0; chunk < chunksnum; chunk++ {
		isReset := false
		if offset == 0 {
			isReset = true
		}
		err = c.UploadOneChunk(bucketName, objPathName, offset, readSize, ncloser, isReset)
		if err != nil {
			c.DeleteObject(bucketName, objPathName)
			return err
		}
		offset += readSize
	}
	isReset := false
	if offset == 0 {
		isReset = true
	}
	if lastslice != 0 {
		err = c.UploadOneChunk(bucketName, objPathName, offset, lastslice, ncloser, isReset)
		if err != nil {
			c.DeleteObject(bucketName, objPathName)
			return err
		}
	}

	return nil
}

// putObjectRequest
func (c *Client) putObjectRequest(uri string, body io.Reader, headers map[string]string) (*http.Response, error) {

	request, err := http.NewRequest("PUT", uri, body)
	if err != nil {
		return nil, err
	}

	for key, value := range headers {
		request.Header.Add(key, value)
	}

	c.setAuthHeader(request)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (c *Client) PutObject(bucketName string, objPathName string, r io.Reader) error {

	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)
	writer, err := bodyWriter.CreateFormFile("file", "file")
	if err != nil {
		return err
	}
	ncloser := ioutil.NopCloser(r)
	_, err = io.Copy(writer, ncloser)
	if err != nil {
		return err
	}
	if e := bodyWriter.Close(); e != nil {
		return e
	}

	path := buildPath([]string{"api/v1/obj", bucketName, objPathName})
	path = path + "/"
	url := c.buildURL(path, nil)
	urlStr := url.String()

	body := bytes.NewReader(bodyBuf.Bytes())
	headers := map[string]string{
		"Content-Type": bodyWriter.FormDataContentType(),
	}
	if file, ok := r.(io.Seeker); ok {
		file.Seek(0, io.SeekStart)
		contentMD5 := MD5sum(r)
		if contentMD5 != "" {
			headers["Content-MD5"] = contentMD5
		}
	}

	resp, err := c.putObjectRequest(urlStr, body, headers)
	if err != nil {
		return nil
	}
	if resp.StatusCode == 200 {
		resp.Body.Close()
		return nil
	}

	defer resp.Body.Close()
	response := buildResponse(resp)
	err = wrapIHarborError(response, "Put object failed,"+urlStr)
	if e := c.makeIfParentPathError(err, bucketName, objPathName); e != nil {
		return e
	}

	// try once again
	body.Seek(0, io.SeekStart)
	resp2, err := c.putObjectRequest(urlStr, body, headers)
	if err != nil {
		return err
	}
	if resp2.StatusCode == 200 {
		resp2.Body.Close()
		return nil
	}

	defer resp2.Body.Close()
	response2 := buildResponse(resp)
	return wrapIHarborError(response2, "Put object failed try once again,"+urlStr)
}

// DeleteObject : delete object
func (c *Client) DeleteObject(bucketName string, objPathName string) error {

	path := buildPath([]string{"api/v1/obj", bucketName, objPathName})
	path = path + "/"
	url := c.buildURL(path, nil)
	urlStr := url.String()

	request, err := http.NewRequest("DELETE", urlStr, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(request)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	response := buildResponse(resp)
	if response.StatusCode == 204 {
		return nil
	}

	return wrapIHarborError(response, "Delete object failed,"+urlStr)
}

// GetObject
// param offset: 对象指定偏移量读；<0忽略
// param size: 读取字节长度；<=0读取到结尾，max 20MB
func (c *Client) GetObject(bucketName string, objPathName string, offset int64, size int64) (io.ReadCloser, int64, error) {

	path := buildPath([]string{"api/v1/obj", bucketName, objPathName})
	path = path + "/"

	var params map[string]string = nil
	if offset >= 0 && size > 0 {
		params = map[string]string{
			"offset": strconv.FormatInt(offset, 10),
			"size":   strconv.FormatInt(size, 10),
		}
	}

	url := c.buildURL(path, params)
	urlStr := url.String()

	request, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(request)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == 200 {
		return resp.Body, resp.ContentLength, nil
	}

	defer resp.Body.Close()
	response := buildResponse(resp)
	return nil, 0, wrapIHarborError(response, "get object failed,"+urlStr)
}

// ObjectAttributes 对象元数据
type ObjectAttributes struct {
	PathName   string `json:"na"`            //全路径
	FileOrDir  bool   `json:"fod"`           //对象（true），目录（false）
	Size       int64  `json:"si"`            //大小，byte
	UploadTime string `json:"ult"`           // 上传时间
	UpdateTime string `json:"upt,omitempty"` // 最后修改时间
}
type ObjectMetaResult struct {
	Obj ObjectAttributes `json:"obj,omitempty"`
}

// GetObjectMeta
// param bucketName: bucket name
// param objPathName: object key
func (c *Client) GetObjectMeta(bucketName string, objPathName string) (*ObjectMetaResult, error) {

	path := buildPath([]string{"api/v1/metadata", bucketName, objPathName})
	path = path + "/"
	url := c.buildURL(path, nil)
	urlStr := url.String()

	request, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(request)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	response := buildResponse(resp)
	ret := ObjectMetaResult{}
	if response.StatusCode == 200 {
		err2 := json.Unmarshal(response.Bytes(), &ret)
		if err2 != nil {
			return nil, errors.New("get object metadata failed," + urlStr + ";err:" + err2.Error())
		}
		return &ret, nil
	}

	return nil, wrapIHarborError(response, "get object metadata failed,"+urlStr)
}

func (c *Client) CreateBucket(bucketName string) error {

	url := c.buildURL("api/v1/buckets/", nil)
	urlStr := url.String()
	body, err := json.Marshal(map[string]string{"name": bucketName})
	if err != nil {
		return err
	}
	request, err := http.NewRequest("POST", urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(request)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	response := buildResponse(resp)
	if response.StatusCode == 201 {
		return nil
	}

	return wrapIHarborError(response, "create bucket failed,"+urlStr)
}

func (c *Client) DeleteBucket(bucketName string) error {

	path := buildPath([]string{"api/v1/buckets", bucketName})
	path = path + "/"
	querys := map[string]string{"by-name": "true"}
	url := c.buildURL(path, querys)
	urlStr := url.String()
	request, err := http.NewRequest("DELETE", urlStr, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(request)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	response := buildResponse(resp)
	if response.StatusCode == 204 {
		return nil
	}

	return wrapIHarborError(response, "delete bucket failed,"+urlStr)
}

func (c *Client) IsObjNotFoundErr(err error) bool {
	if err == nil {
		return false
	}

	if e, ok := err.(Error); ok {
		if e.Code == ErrCodeNoSuchKey || e.StatusCode == 404 {
			return true
		}
	}

	return false
}

func (c *Client) IsNoParentPathErr(err error) bool {
	if err == nil {
		return false
	}

	if e, ok := err.(Error); ok {
		if e.Code == ErrCodeNoParentPath {
			return true
		}
	}

	return false
}

type ListObjectsResult struct {
	BucketName string             `json:"bucket_name,omitempty"`
	DirPath    string             `json:"dir_path,omitempty"`
	Count      int                `json:"count,omitempty"`
	Next       string             `json:"next,omitempty"`
	Previous   string             `json:"previous,omitempty"`
	Files      []ObjectAttributes `json:"files"`
}

func (ldr ListObjectsResult) HasNext() bool {
	return ldr.Next != ""
}

func (ldr ListObjectsResult) HasPrevious() bool {
	return ldr.Previous != ""
}

func (ldr ListObjectsResult) PreviousURL() string {
	return ldr.Previous
}

func (ldr ListObjectsResult) NextURL() string {
	return ldr.Next
}

// ListObjects List the objects in the directory
// param dirPath: dir
// param offset limit: starting from "offset", list "limit" object£»
//		ignore "offset" and "limit" if < 0, limit <= 1000
// param onlyObject: true(only list object); false(ignore)
func (c *Client) ListObjects(bucketName, dirPath string, offset, limit int, onlyObject bool) (*ListObjectsResult, error) {

	path := buildPath([]string{"api/v1/dir", bucketName, dirPath})
	path = path + "/"

	var params = make(map[string]string)
	if offset > 0 {
		params["offset"] = strconv.Itoa(offset)
	}

	if limit > 0 {
		params["limit"] = strconv.Itoa(limit)
	}

	if onlyObject {
		params["only-obj"] = "true"
	}
	url := c.buildURL(path, params)
	urlStr := url.String()
	println(urlStr)
	return c.ListObjectsByURL(urlStr)
}

func (c *Client) ListObjectsByURL(url string) (*ListObjectsResult, error) {

	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(request)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	response := buildResponse(resp)
	if response.StatusCode == 200 {
		return c.buildListObjectsResult(response.Bytes())
	}

	return nil, wrapIHarborError(response, "list objects failed,"+url)
}

// buildListObjectsResult
func (c *Client) buildListObjectsResult(data []byte) (*ListObjectsResult, error) {

	lbr := ListObjectsResult{}
	err2 := json.Unmarshal(data, &lbr)
	if err2 != nil {
		return nil, err2
	}
	return &lbr, nil
}

type ObjectProperties struct {
	Key          string `json:"Key"`
	IsObject     bool   `json:"IsObject"`
	Size         int64  `json:"Size"`
	LastModified string `json:"LastModified"`
	ETag         string `json:"ETag"`
}

type ListBucketObjectsResult struct {
	Name                  string             `json:"Name"`
	Prefix                string             `json:"Prefix,omitempty"`
	Delimiter             string             `json:"Delimiter,omitempty"`
	KeyCount              int                `json:"KeyCount"`
	IsTruncated           bool               `json:"IsTruncated"`
	MaxKeys               int64              `json:"MaxKeys"`
	ContinuationToken     string             `json:"ContinuationToken,omitempty"`
	NextContinuationToken string             `json:"NextContinuationToken,omitempty"`
	Next                  string             `json:"Next,omitempty"`
	Previous              string             `json:"Previous,omitempty"`
	Contents              []ObjectProperties `json:"Contents"`
}

func (ldr ListBucketObjectsResult) HasNext() bool {
	return ldr.IsTruncated
}

// ListBucketObjects List the objects in the directory
// param delimiter: "" or "/"
// param maxKeys: max number of object will list£»ignore  if < 0, maxKeys <= 1000
// param continuationToken: "" ignore
func (c *Client) ListBucketObjects(bucketName, prefix, delimiter, continuationToken string, maxKeys int) (*ListBucketObjectsResult, error) {

	path := buildPath([]string{"api/v1/list/bucket", bucketName})
	path = path + "/"

	var params = make(map[string]string)
	params["prefix"] = prefix
	if delimiter == "/" {
		params["delimiter"] = delimiter
	}

	if maxKeys > 0 {
		params["max-keys"] = strconv.Itoa(maxKeys)
	}

	if continuationToken != "" {
		params["continuation-token"] = continuationToken
	}
	url := c.buildURL(path, params)
	urlStr := url.String()
	println(urlStr)

	request, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(request)
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	response := buildResponse(resp)
	if response.StatusCode == 200 {
		lbr := ListBucketObjectsResult{}
		err2 := json.Unmarshal(response.Bytes(), &lbr)
		if err2 != nil {
			return nil, err2
		}
		return &lbr, nil
	}

	return nil, wrapIHarborError(response, "list objects failed,"+urlStr)
}
