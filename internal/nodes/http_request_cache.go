package nodes

import (
	"bytes"
	"errors"
	"github.com/TeaOSLab/EdgeCommon/pkg/rpc/pb"
	"github.com/TeaOSLab/EdgeNode/internal/caches"
	"github.com/TeaOSLab/EdgeNode/internal/compressions"
	"github.com/TeaOSLab/EdgeNode/internal/goman"
	"github.com/TeaOSLab/EdgeNode/internal/remotelogs"
	"github.com/TeaOSLab/EdgeNode/internal/rpc"
	"github.com/TeaOSLab/EdgeNode/internal/utils"
	rangeutils "github.com/TeaOSLab/EdgeNode/internal/utils/ranges"
	"github.com/iwind/TeaGo/types"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 读取缓存
func (this *HTTPRequest) doCacheRead(useStale bool) (shouldStop bool) {
	this.cacheCanTryStale = false

	var cachePolicy = this.ReqServer.HTTPCachePolicy
	if cachePolicy == nil || !cachePolicy.IsOn {
		return
	}

	if this.web.Cache == nil || !this.web.Cache.IsOn || (len(cachePolicy.CacheRefs) == 0 && len(this.web.Cache.CacheRefs) == 0) {
		return
	}

	// 判断是否在预热
	if (strings.HasPrefix(this.RawReq.RemoteAddr, "127.") || strings.HasPrefix(this.RawReq.RemoteAddr, "[::1]")) && this.RawReq.Header.Get("X-Cache-Action") == "preheat" {
		return
	}

	// 添加 X-Cache Header
	var addStatusHeader = this.web.Cache.AddStatusHeader
	if addStatusHeader {
		defer func() {
			cacheStatus := this.varMapping["cache.status"]
			if cacheStatus != "HIT" {
				this.writer.Header().Set("X-Cache", cacheStatus)
			}
		}()
	}

	// 检查服务独立的缓存条件
	refType := ""
	for _, cacheRef := range this.web.Cache.CacheRefs {
		if !cacheRef.IsOn ||
			cacheRef.Conds == nil ||
			!cacheRef.Conds.HasRequestConds() {
			continue
		}
		if cacheRef.Conds.MatchRequest(this.Format) {
			if cacheRef.IsReverse {
				return
			}
			this.cacheRef = cacheRef
			refType = "server"
			break
		}
	}
	if this.cacheRef == nil && !this.web.Cache.DisablePolicyRefs {
		// 检查策略默认的缓存条件
		for _, cacheRef := range cachePolicy.CacheRefs {
			if !cacheRef.IsOn ||
				cacheRef.Conds == nil ||
				!cacheRef.Conds.HasRequestConds() {
				continue
			}
			if cacheRef.Conds.MatchRequest(this.Format) {
				if cacheRef.IsReverse {
					return
				}
				this.cacheRef = cacheRef
				refType = "policy"
				break
			}
		}
	}

	if this.cacheRef == nil {
		return
	}

	// 校验请求
	if !this.cacheRef.MatchRequest(this.RawReq) {
		this.cacheRef = nil
		return
	}

	// 相关变量
	this.varMapping["cache.policy.name"] = cachePolicy.Name
	this.varMapping["cache.policy.id"] = strconv.FormatInt(cachePolicy.Id, 10)
	this.varMapping["cache.policy.type"] = cachePolicy.Type

	// Cache-Pragma
	if this.cacheRef.EnableRequestCachePragma {
		if this.RawReq.Header.Get("Cache-Control") == "no-cache" || this.RawReq.Header.Get("Pragma") == "no-cache" {
			this.cacheRef = nil
			return
		}
	}

	// TODO 支持Vary Header

	// 缓存标签
	var tags = []string{}

	// 检查是否有缓存
	var key = this.Format(this.cacheRef.Key)
	if len(key) == 0 {
		this.cacheRef = nil
		return
	}
	var method = this.Method()
	if method != http.MethodGet {
		key += caches.SuffixMethod + method
		tags = append(tags, strings.ToLower(method))
	}

	this.cacheKey = key
	this.varMapping["cache.key"] = key

	// 读取缓存
	storage := caches.SharedManager.FindStorageWithPolicy(cachePolicy.Id)
	if storage == nil {
		this.cacheRef = nil
		return
	}
	this.writer.cacheStorage = storage

	// 判断是否在Purge
	if this.web.Cache.PurgeIsOn && strings.ToUpper(this.RawReq.Method) == "PURGE" && this.RawReq.Header.Get("X-Edge-Purge-Key") == this.web.Cache.PurgeKey {
		this.varMapping["cache.status"] = "PURGE"

		var subKeys = []string{
			key,
			key + caches.SuffixMethod + "HEAD",
			key + caches.SuffixWebP,
			key + caches.SuffixPartial,
		}
		// TODO 根据实际缓存的内容进行组合
		for _, encoding := range compressions.AllEncodings() {
			subKeys = append(subKeys, key+caches.SuffixCompression+encoding)
			subKeys = append(subKeys, key+caches.SuffixWebP+caches.SuffixCompression+encoding)
		}
		for _, subKey := range subKeys {
			err := storage.Delete(subKey)
			if err != nil {
				remotelogs.Error("HTTP_REQUEST_CACHE", "purge failed: "+err.Error())
			}
		}

		// 通过API节点清除别节点上的的Key
		// TODO 改为队列，不需要每个请求都使用goroutine
		goman.New(func() {
			rpcClient, err := rpc.SharedRPC()
			if err == nil {
				for _, rpcServerService := range rpcClient.ServerRPCList() {
					_, err = rpcServerService.PurgeServerCache(rpcClient.Context(), &pb.PurgeServerCacheRequest{
						Domains:  []string{this.ReqHost},
						Keys:     []string{key},
						Prefixes: nil,
					})
					if err != nil {
						remotelogs.Error("HTTP_REQUEST_CACHE", "purge failed: "+err.Error())
					}
				}
			}
		})

		return true
	}

	// 调用回调
	this.onRequest()
	if this.writer.isFinished {
		return
	}

	var reader caches.Reader
	var err error

	var rangeHeader = this.RawReq.Header.Get("Range")
	var isPartialRequest = len(rangeHeader) > 0

	// 检查是否支持WebP
	var webPIsEnabled = false
	var isHeadMethod = method == http.MethodHead
	if !isPartialRequest &&
		!isHeadMethod &&
		this.web.WebP != nil &&
		this.web.WebP.IsOn &&
		this.web.WebP.MatchRequest(filepath.Ext(this.Path()), this.Format) &&
		this.web.WebP.MatchAccept(this.RawReq.Header.Get("Accept")) {
		webPIsEnabled = true
	}

	// 检查压缩缓存
	if !isPartialRequest && !isHeadMethod && reader == nil {
		if this.web.Compression != nil && this.web.Compression.IsOn {
			_, encoding, ok := this.web.Compression.MatchAcceptEncoding(this.RawReq.Header.Get("Accept-Encoding"))
			if ok {
				// 检查支持WebP的压缩缓存
				if webPIsEnabled {
					reader, _ = storage.OpenReader(key+caches.SuffixWebP+caches.SuffixCompression+encoding, useStale, false)
					if reader != nil {
						tags = append(tags, "webp", encoding)
					}
				}

				// 检查普通压缩缓存
				if reader == nil {
					reader, _ = storage.OpenReader(key+caches.SuffixCompression+encoding, useStale, false)
					if reader != nil {
						tags = append(tags, encoding)
					}
				}
			}
		}
	}

	// 检查WebP
	if !isPartialRequest &&
		!isHeadMethod &&
		reader == nil &&
		webPIsEnabled {
		reader, _ = storage.OpenReader(key+caches.SuffixWebP, useStale, false)
		if reader != nil {
			this.writer.cacheReaderSuffix = caches.SuffixWebP
			tags = append(tags, "webp")
		}
	}

	// 检查正常的文件
	var isPartialCache = false
	var partialRanges []rangeutils.Range
	if reader == nil {
		reader, err = storage.OpenReader(key, useStale, false)
		if err != nil && this.cacheRef.AllowPartialContent {
			pReader, ranges := this.tryPartialReader(storage, key, useStale, rangeHeader)
			if pReader != nil {
				isPartialCache = true
				reader = pReader
				partialRanges = ranges
				err = nil
			}
		}

		if err != nil {
			if err == caches.ErrNotFound {
				// cache相关变量
				this.varMapping["cache.status"] = "MISS"

				if !useStale && this.web.Cache.Stale != nil && this.web.Cache.Stale.IsOn {
					this.cacheCanTryStale = true
				}
				return
			}

			if !this.canIgnore(err) {
				remotelogs.Warn("HTTP_REQUEST_CACHE", this.URL()+": read from cache failed: open cache failed: "+err.Error())
			}
			return
		}
	}

	defer func() {
		if !this.writer.DelayRead() {
			_ = reader.Close()
		}
	}()

	if useStale {
		this.varMapping["cache.status"] = "STALE"
		this.logAttrs["cache.status"] = "STALE"
	} else {
		this.varMapping["cache.status"] = "HIT"
		this.logAttrs["cache.status"] = "HIT"
	}

	// 准备Buffer
	var fileSize = reader.BodySize()
	var totalSizeString = types.String(fileSize)
	if isPartialCache {
		fileSize = reader.(*caches.PartialFileReader).MaxLength()
		if totalSizeString == "0" {
			totalSizeString = "*"
		}
	}

	var pool = this.bytePool(fileSize)
	var buf = pool.Get()
	defer func() {
		pool.Put(buf)
	}()

	// 读取Header
	var headerBuf = []byte{}
	this.writer.SetSentHeaderBytes(reader.HeaderSize())
	err = reader.ReadHeader(buf, func(n int) (goNext bool, err error) {
		headerBuf = append(headerBuf, buf[:n]...)
		for {
			nIndex := bytes.Index(headerBuf, []byte{'\n'})
			if nIndex >= 0 {
				row := headerBuf[:nIndex]
				spaceIndex := bytes.Index(row, []byte{':'})
				if spaceIndex <= 0 {
					return false, errors.New("invalid header '" + string(row) + "'")
				}

				this.writer.Header().Set(string(row[:spaceIndex]), string(row[spaceIndex+1:]))
				headerBuf = headerBuf[nIndex+1:]
			} else {
				break
			}
		}
		return true, nil
	})
	if err != nil {
		if !this.canIgnore(err) {
			remotelogs.Warn("HTTP_REQUEST_CACHE", this.URL()+": read from cache failed: read header failed: "+err.Error())
		}
		return
	}

	// 设置cache.age变量
	var age = strconv.FormatInt(utils.UnixTime()-reader.LastModified(), 10)
	this.varMapping["cache.age"] = age

	if addStatusHeader {
		if useStale {
			this.writer.Header().Set("X-Cache", "STALE, "+refType+", "+reader.TypeName())
		} else {
			this.writer.Header().Set("X-Cache", "HIT, "+refType+", "+reader.TypeName())
		}
	} else {
		this.writer.Header().Del("X-Cache")
	}
	if this.web.Cache.AddAgeHeader {
		this.writer.Header().Set("Age", age)
	}

	// ETag
	// 这里强制设置ETag，如果先前源站设置了ETag，将会被覆盖，避免因为源站的ETag导致源站返回304 Not Modified
	var respHeader = this.writer.Header()
	var eTag = ""
	var lastModifiedAt = reader.LastModified()
	if lastModifiedAt > 0 {
		if len(tags) > 0 {
			eTag = "\"" + strconv.FormatInt(lastModifiedAt, 10) + "_" + strings.Join(tags, "_") + "\""
		} else {
			eTag = "\"" + strconv.FormatInt(lastModifiedAt, 10) + "\""
		}
		respHeader.Del("Etag")
		if !isPartialCache {
			respHeader["ETag"] = []string{eTag}
		}
	}

	// 支持 Last-Modified
	// 这里强制设置Last-Modified，如果先前源站设置了Last-Modified，将会被覆盖，避免因为源站的Last-Modified导致源站返回304 Not Modified
	var modifiedTime = ""
	if lastModifiedAt > 0 {
		modifiedTime = time.Unix(utils.GMTUnixTime(lastModifiedAt), 0).Format("Mon, 02 Jan 2006 15:04:05") + " GMT"
		if !isPartialCache {
			respHeader.Set("Last-Modified", modifiedTime)
		}
	}

	// 支持 If-None-Match
	if !isPartialCache && len(eTag) > 0 && this.requestHeader("If-None-Match") == eTag {
		// 自定义Header
		this.processResponseHeaders(http.StatusNotModified)
		this.writer.WriteHeader(http.StatusNotModified)
		this.isCached = true
		this.cacheRef = nil
		this.writer.SetOk()
		return true
	}

	// 支持 If-Modified-Since
	if !isPartialCache && len(modifiedTime) > 0 && this.requestHeader("If-Modified-Since") == modifiedTime {
		// 自定义Header
		this.processResponseHeaders(http.StatusNotModified)
		this.writer.WriteHeader(http.StatusNotModified)
		this.isCached = true
		this.cacheRef = nil
		this.writer.SetOk()
		return true
	}

	this.processResponseHeaders(reader.Status())
	this.addExpiresHeader(reader.ExpiresAt())

	// 输出Body
	if this.RawReq.Method == http.MethodHead {
		this.writer.WriteHeader(reader.Status())
	} else {
		ifRangeHeaders, ok := this.RawReq.Header["If-Range"]
		var supportRange = true
		if ok {
			supportRange = false
			for _, v := range ifRangeHeaders {
				if v == this.writer.Header().Get("ETag") || v == this.writer.Header().Get("Last-Modified") {
					supportRange = true
					break
				}
			}
		}

		// 支持Range
		var ranges = partialRanges
		if supportRange {
			if len(rangeHeader) > 0 {
				if fileSize == 0 {
					this.processResponseHeaders(http.StatusRequestedRangeNotSatisfiable)
					this.writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return true
				}

				if len(ranges) == 0 {
					ranges, ok = httpRequestParseRangeHeader(rangeHeader)
					if !ok {
						this.processResponseHeaders(http.StatusRequestedRangeNotSatisfiable)
						this.writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
						return true
					}
				}
				if len(ranges) > 0 {
					for k, r := range ranges {
						r2, ok := r.Convert(fileSize)
						if !ok {
							this.processResponseHeaders(http.StatusRequestedRangeNotSatisfiable)
							this.writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
							return true
						}

						ranges[k] = r2
					}
				}
			}
		}

		if len(ranges) == 1 {
			respHeader.Set("Content-Range", ranges[0].ComposeContentRangeHeader(totalSizeString))
			respHeader.Set("Content-Length", strconv.FormatInt(ranges[0].Length(), 10))
			this.writer.WriteHeader(http.StatusPartialContent)

			err = reader.ReadBodyRange(buf, ranges[0].Start(), ranges[0].End(), func(n int) (goNext bool, err error) {
				_, err = this.writer.Write(buf[:n])
				if err != nil {
					return false, errWritingToClient
				}
				return true, nil
			})
			if err != nil {
				this.varMapping["cache.status"] = "MISS"

				if err == caches.ErrInvalidRange {
					this.processResponseHeaders(http.StatusRequestedRangeNotSatisfiable)
					this.writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return true
				}
				if !this.canIgnore(err) {
					remotelogs.Warn("HTTP_REQUEST_CACHE", this.URL()+": read from cache failed: "+err.Error())
				}
				return
			}
		} else if len(ranges) > 1 {
			var boundary = httpRequestGenBoundary()
			respHeader.Set("Content-Type", "multipart/byteranges; boundary="+boundary)
			respHeader.Del("Content-Length")
			contentType := respHeader.Get("Content-Type")

			this.writer.WriteHeader(http.StatusPartialContent)

			for index, r := range ranges {
				if index == 0 {
					_, err = this.writer.WriteString("--" + boundary + "\r\n")
				} else {
					_, err = this.writer.WriteString("\r\n--" + boundary + "\r\n")
				}
				if err != nil {
					// 不提示写入客户端错误
					return true
				}

				_, err = this.writer.WriteString("Content-Range: " + r.ComposeContentRangeHeader(totalSizeString) + "\r\n")
				if err != nil {
					// 不提示写入客户端错误
					return true
				}

				if len(contentType) > 0 {
					_, err = this.writer.WriteString("Content-Type: " + contentType + "\r\n\r\n")
					if err != nil {
						// 不提示写入客户端错误
						return true
					}
				}

				err := reader.ReadBodyRange(buf, r.Start(), r.End(), func(n int) (goNext bool, err error) {
					_, err = this.writer.Write(buf[:n])
					if err != nil {
						return false, errWritingToClient
					}
					return true, nil
				})
				if err != nil {
					if !this.canIgnore(err) {
						remotelogs.Warn("HTTP_REQUEST_CACHE", this.URL()+": read from cache failed: "+err.Error())
					}
					return true
				}
			}

			_, err = this.writer.WriteString("\r\n--" + boundary + "--\r\n")
			if err != nil {
				this.varMapping["cache.status"] = "MISS"

				// 不提示写入客户端错误
				return true
			}
		} else { // 没有Range
			var resp = &http.Response{Body: reader}
			this.writer.Prepare(resp, fileSize, reader.Status(), false)
			this.writer.WriteHeader(reader.Status())

			_, err = io.CopyBuffer(this.writer, resp.Body, buf)
			if err == io.EOF {
				err = nil
			}
			if err != nil {
				this.varMapping["cache.status"] = "MISS"

				if !this.canIgnore(err) {
					remotelogs.Warn("HTTP_REQUEST_CACHE", this.URL()+": read from cache failed: read body failed: "+err.Error())
				}
				return
			}
		}
	}

	this.isCached = true
	this.cacheRef = nil

	this.writer.SetOk()

	return true
}

// 设置Expires Header
func (this *HTTPRequest) addExpiresHeader(expiresAt int64) {
	if this.cacheRef.ExpiresTime != nil && this.cacheRef.ExpiresTime.IsPrior && this.cacheRef.ExpiresTime.IsOn {
		if this.cacheRef.ExpiresTime.Overwrite || len(this.writer.Header().Get("Expires")) == 0 {
			if this.cacheRef.ExpiresTime.AutoCalculate {
				this.writer.Header().Set("Expires", time.Unix(utils.GMTUnixTime(expiresAt), 0).Format("Mon, 2 Jan 2006 15:04:05")+" GMT")
			} else if this.cacheRef.ExpiresTime.Duration != nil {
				var duration = this.cacheRef.ExpiresTime.Duration.Duration()
				if duration > 0 {
					this.writer.Header().Set("Expires", utils.GMTTime(time.Now().Add(duration)).Format("Mon, 2 Jan 2006 15:04:05")+" GMT")
				}
			}
		}
	}
}

// 尝试读取区间缓存
func (this *HTTPRequest) tryPartialReader(storage caches.StorageInterface, key string, useStale bool, rangeHeader string) (caches.Reader, []rangeutils.Range) {
	// 尝试读取Partial cache
	if len(rangeHeader) == 0 {
		return nil, nil
	}

	ranges, ok := httpRequestParseRangeHeader(rangeHeader)
	if !ok {
		return nil, nil
	}

	pReader, pErr := storage.OpenReader(key+caches.SuffixPartial, useStale, true)
	if pErr != nil {
		return nil, nil
	}

	partialReader, ok := pReader.(*caches.PartialFileReader)
	if !ok {
		_ = pReader.Close()
		return nil, nil
	}
	var isOk = false
	defer func() {
		if !isOk {
			_ = pReader.Close()
		}
	}()

	// 检查范围
	for index, r := range ranges {
		r1, ok := r.Convert(partialReader.MaxLength())
		if !ok {
			return nil, nil
		}
		r2, ok := partialReader.ContainsRange(r1)
		if !ok {
			return nil, nil
		}
		ranges[index] = r2
	}

	isOk = true
	return pReader, ranges
}
