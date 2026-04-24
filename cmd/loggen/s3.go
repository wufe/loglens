package main

import (
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

var (
	s3Buckets = []string{
		"lock-generic", "backup-acme", "archive-zeta", "media-prod",
		"logs-prod", "snapshots-enterprise", "tenant-northwind",
		"vault-blueocean", "cold-storage-01", "releases-fabrikam",
	}
	s3Tenants = []struct{ id, name string }{
		{"bc578211-c521-4e3a-84b1-f9b4fd054917", "tenantcore"},
		{"4a0f91c8-81d2-4b77-aae4-32f69fd44aa1", "northwind"},
		{"0e4e3b62-3a1c-4a53-8f86-9c2a7cf1f1ac", "fabrikam"},
		{"f10c6d62-9999-4c51-bd7b-1ad3fc2d0e91", "acme-corp"},
	}
	s3Methods = []struct {
		method string
		api    string
	}{
		{"PUT", "PutObject"},
		{"GET", "GetObject"},
		{"HEAD", "HeadObject"},
		{"DELETE", "DeleteObject"},
		{"POST", "UploadPart"},
	}
	s3UserAgents = []string{
		"APN/1.0 appx/1.0 client/13.0",
		"aws-sdk-go-v2/1.25.0 os/linux lang/go#1.22.1",
		"rclone/v1.66.0",
		"MinIO (linux; amd64) minio-go/v7.0.70",
		"Boto3/1.34.20 Python/3.11.8",
	}
	// Weighted status buckets: most requests succeed, but we want enough
	// warn/error logs to exercise level-dependent highlighting.
	s3StatusMix = []struct {
		status int
		level  string
		weight int
	}{
		{200, "info", 55},
		{206, "info", 15},
		{204, "info", 5},
		{404, "warn", 8},
		{403, "warn", 5},
		{499, "warn", 6},
		{500, "error", 4},
		{503, "error", 2},
	}
	s3Errors = []string{
		"handling upload request: saving payload: operation error S3: PutObject, https response error StatusCode: 0, RequestID: , HostID: , canceled, context canceled: ClientClosedRequest",
		"fetching object metadata: operation error S3: HeadObject, https response error StatusCode: 404, RequestID: , HostID: , NoSuchKey: The specified key does not exist.",
		"authorizing request: tenant quota exceeded for bucket, denying write",
		"storage backend error: dial tcp: i/o timeout after 30s",
		"operation error S3: PutObject, failed to compute checksum: unexpected EOF",
	}
)

// buildS3JSONLine synthesizes a single DD-instrumented my-s3-gw-style JSON log
// line with a long URL (usually >220 bytes) so the line wraps in typical
// terminal widths. Every field varies per-call.
func buildS3JSONLine() string {
	traceID := randHex(16)
	spanID := randHex(8)
	requestID := randHex(16)
	bucket := s3Buckets[rand.IntN(len(s3Buckets))]
	tenant := s3Tenants[rand.IntN(len(s3Tenants))]
	mth := s3Methods[rand.IntN(len(s3Methods))]
	ua := s3UserAgents[rand.IntN(len(s3UserAgents))]

	// Long object path: fixed S3-backup-like skeleton with four varying UUIDs,
	// deep directories, a 5-digit chunk index, and a 32-byte hex fingerprint.
	uuid1 := randUUID()
	uuid2 := randUUID()
	uuid3 := randUUID()
	uuid4 := randUUID()
	chunkIdx := rand.IntN(100000)
	chunkHex := randHex(16)
	objectName := fmt.Sprintf(
		"appx/archive/%s-backup/%s/%s/data.v%d/Data/{%s}/{%s}/%05d_%s_00000000000000000000000000000000",
		bucket, uuid1, uuid2, 2+rand.IntN(2), uuid3, uuid4, chunkIdx, chunkHex,
	)
	urlPath := "/" + bucket + "/" + urlEscapePath(objectName)

	// Pick a status; correlate level/error.
	st := pickStatus()
	level := st.level
	now := time.Now().UTC()
	endT := now
	startT := now.Add(-time.Duration(rand.IntN(180_000)) * time.Millisecond)
	durMs := endT.Sub(startT).Milliseconds()

	sizeReq := 1024 + rand.IntN(2_000_000)
	sizeResp := 64 + rand.IntN(512)
	ip := fmt.Sprintf("%d.%d.%d.%d", 10+rand.IntN(240), rand.IntN(256), rand.IntN(256), 1+rand.IntN(254))
	accessKey := randAlphaNum(20)

	// Errors only for >=400 statuses.
	errField := ""
	if st.status >= 400 {
		errField = fmt.Sprintf(`,"error":%q`, s3Errors[rand.IntN(len(s3Errors))])
	}

	// Build the headers sub-object. Keep the common set seen in the
	// real-world sample so detectors see realistic structure.
	headers := fmt.Sprintf(`{"X-Amz-Object-Lock-Mode":"COMPLIANCE","X-App-Tenant-Name":%q,"X-Forwarded-Host":"s3.%s.example.com","Content-Length":"%d","X-Amz-Checksum-Sha256":%q,"X-Amz-Object-Lock-Retain-Until-Date":%q,"X-Forwarded-Proto":"https","X-Scheme":"https","Content-Type":"application/octet-stream","Authorization":"AWS4-HMAC-SHA256 Credential=%s/%s/us-east-1/s3/aws4_request,SignedHeaders=content-length;content-type;host;user-agent;x-amz-checksum-sha256;x-amz-content-sha256;x-amz-date,Signature=REDACTED","User-Agent":%q,"X-Amz-Content-Sha256":%q,"X-Forwarded-Scheme":"https","X-Amz-Storage-Class":"STANDARD","X-Forwarded-For":%q,"X-Amz-Sdk-Checksum-Algorithm":"SHA256","X-Forwarded-Port":"443","X-App-Tenant-Id":%q,"X-Request-Id":%q,"X-Amz-Date":%q,"X-Real-Ip":%q}`,
		tenant.name,
		tenant.name,
		sizeReq,
		base64Like(32),
		endT.Add(15*24*time.Hour).Format(time.RFC3339),
		accessKey,
		endT.Format("20060102"),
		ua,
		randHex(32),
		ip,
		tenant.id,
		requestID,
		endT.Format("20060102T150405Z"),
		ip,
	)

	return fmt.Sprintf(
		`{"level":%q,"dd.trace_id":%q,"dd.span_id":%q,"dd.service":"my-s3-gw","dd.env":"prod-s3-gw","dd.version":"1.0.0-258efafd","service.name":"my-s3-gw","service.version":"1.0.0-258efafd","service.env":"prod-s3-gw","http.method":%q,"http.url":%q,"http.headers":%s,"http.useragent":%q,"app.api_name":%q,"app.bucket_name":%q,"app.object_name":%q,"app.vhs":false,"app.api_key":"REDACTED","app.tenant_id":%q,"app.tenant_name":%q,"network.client.ip":%q,"app.upload_target":"cache","http.request_id":%q,"traceID":%q,"spanID":%q%s,"http.size_request":%d,"http.size_response":%d,"http.size_content":0,"http.timestamp_start":%q,"http.timestamp_end":%q,"http.duration_ms":%d,"http.status_code":%d,"time":%q,"message":"[%s] %s %s %d"}`,
		level, traceID, spanID,
		mth.method, urlPath,
		headers,
		ua,
		mth.api, bucket, objectName,
		tenant.id, tenant.name,
		ip,
		requestID, traceID, spanID,
		errField,
		sizeReq, sizeResp,
		startT.Format(time.RFC3339), endT.Format(time.RFC3339),
		durMs, st.status,
		endT.Format(time.RFC3339),
		accessKey, mth.method, urlPath, st.status,
	)
}

func pickStatus() (chosen struct {
	status int
	level  string
	weight int
}) {
	total := 0
	for _, s := range s3StatusMix {
		total += s.weight
	}
	r := rand.IntN(total)
	for _, s := range s3StatusMix {
		if r < s.weight {
			return s
		}
		r -= s.weight
	}
	return s3StatusMix[0]
}

func randHex(bytes int) string {
	b := make([]byte, bytes)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	return hex.EncodeToString(b)
}

func randUUID() string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	// RFC 4122 variant/version bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

const alphanumChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randAlphaNum(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphanumChars[rand.IntN(len(alphanumChars))]
	}
	return string(b)
}

const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func base64Like(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = base64Chars[rand.IntN(len(base64Chars))]
	}
	if n >= 2 {
		b[n-1] = '='
	}
	return string(b)
}

// urlEscapePath percent-encodes `{` and `}` like the appx client does, but
// leaves slashes intact so the resulting path keeps its hierarchy (and its
// length).
func urlEscapePath(p string) string {
	r := strings.NewReplacer("{", "%7B", "}", "%7D")
	return r.Replace(p)
}
