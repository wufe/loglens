package pattern

import (
	"slices"
	"strings"
	"testing"
)

// Test data here is anonymized from real production logs. The shape (token
// counts, structural punctuation, presence/absence of trailing fields) is
// preserved so we test the algorithm on realistic-looking inputs; every
// identifier (UUIDs, hashes, IPs, hostnames, paths, tenant names, service
// names, product names) is replaced with a generic placeholder of the same
// shape. Nothing in this file should reflect a real product, customer, or
// internal architecture term.

const podPrefix = `[pod/myservice-0123456789-aaaaa/myservice]`

// ---- Structured JSON: routing-policy template, varying tenant_id only ----

var routingPolicyLines = []string{
	podPrefix + ` {"level":"info","tenant_id":"00000000-0000-0000-0000-000000000000","upload_to_primary":true,"upload_to_secondary":false,"time":"2026-05-25T00:12:52Z","message":"determined upload destination based on routing policy"}`,
	podPrefix + ` {"level":"info","tenant_id":"11111111-2222-3333-4444-555555555555","upload_to_primary":true,"upload_to_secondary":false,"time":"2026-05-25T00:12:53Z","message":"determined upload destination based on routing policy"}`,
	podPrefix + ` {"level":"info","tenant_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","upload_to_primary":true,"upload_to_secondary":false,"time":"2026-05-25T00:12:54Z","message":"determined upload destination based on routing policy"}`,
}

// ---- Structured JSON: route-resolved template, varying gateway_id / redundancy_class ----
// Same tenant_name across lines (varying tenant_name would split the cluster — that's
// a known limitation of mask-then-cluster on literal field values).

var routeResolvedLines = []string{
	podPrefix + ` {"level":"info","gateway_id":"99999999-8888-7777-6666-555555555555","tenant_name":"tenantA","storage_class":"STANDARD","redundancy_class":"11111111-2222-3333-4444-555555555555","keep":true,"upload_to_primary":true,"primary_url":"https://primary.example.com/","time":"2026-05-25T00:12:52Z","message":"route resolved successfully"}`,
	podPrefix + ` {"level":"info","gateway_id":"88888888-7777-6666-5555-444444444444","tenant_name":"tenantA","storage_class":"STANDARD","redundancy_class":"22222222-3333-4444-5555-666666666666","keep":true,"upload_to_primary":true,"primary_url":"https://primary.example.com/","time":"2026-05-25T00:12:53Z","message":"route resolved successfully"}`,
}

// ---- Structured JSON: PUT requests, varying paths / request IDs ----

var putRequestLines = []string{
	podPrefix + ` {"level":"info","http.request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","traceID":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","spanID":"cccccccccccccccc","time":"2026-05-25T00:12:52Z","message":"[ABcdefGHijklMNopQRstUVwxYZ012345] PUT /obj-a/appx/data/key1/%7B11111111-2222-3333-4444-555555555555%7D/file1 200"}`,
	podPrefix + ` {"level":"info","http.request_id":"dddddddddddddddddddddddddddddddd","traceID":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","spanID":"ffffffffffffffff","time":"2026-05-25T00:12:53Z","message":"[ZYxwvuTSrqponMLkjihGFedcba987654] PUT /obj-b/appx/data/key2/%7B66666666-7777-8888-9999-aaaaaaaaaaaa%7D/file2 200"}`,
}

// ---- 14-line nginx + access-log sample, structurally anonymized.
// Each line starts with <epoch_ms>\t<iso_ts>\t<actual>, mirroring the original.
// 8 distinct templates expected (T1..T8 with counts 1/3/2/4/1/1/1/1). ----

var (
	// T1: nginx upstream timed out while CONNECTING (1 line)
	t1Lines = []string{
		"1779695786960\t2026-05-25T07:56:26.960Z\t2026/05/25 07:56:26 [error] 2833#2833: *70725918 upstream timed out (110: Operation timed out) while connecting to upstream, client: 10.0.0.1, server: host1.example.com, request: \"GET /robots.txt HTTP/1.1\", upstream: \"http://10.0.0.99:9159/robots.txt\", host: \"host1.example.com\"",
	}

	// T2: nginx upstream timed out while READING + has referrer (3 lines)
	t2Lines = []string{
		"1779694117640\t2026-05-25T07:28:37.640Z\t2026/05/25 07:28:37 [error] 13261#13261: *186098572 upstream timed out (110: Operation timed out) while reading upstream, client: 10.0.1.1, server: host2.example.cloud.io, request: \"GET /assets/id.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/20260429171318_file_a.mp4 HTTP/2.0\", upstream: \"http://10.0.1.73:443/assets/id.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/20260429171318_file_a.mp4\", host: \"host2.example.cloud.io\", referrer: \"https://platform.example.com/\"",
		"1779694113783\t2026-05-25T07:28:33.783Z\t2026/05/25 07:28:33 [error] 12931#12931: *188706094 upstream timed out (110: Operation timed out) while reading upstream, client: 10.0.0.1, server: host2.example.cloud.io, request: \"GET /assets/id.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/20260423160717_file_b.mp4 HTTP/2.0\", upstream: \"http://10.0.0.163:443/assets/id.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/20260423160717_file_b.mp4\", host: \"host2.example.cloud.io\", referrer: \"https://platform.example.com/\"",
		"1779694101678\t2026-05-25T07:28:21.678Z\t2026/05/25 07:28:21 [error] 14019#14019: *188787956 upstream timed out (110: Operation timed out) while reading upstream, client: 10.0.1.0, server: host2.example.cloud.io, request: \"GET /assets/id.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/20260428094011_file_c.mp4 HTTP/2.0\", upstream: \"http://10.0.1.73:443/assets/id.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/20260428094011_file_c.mp4\", host: \"host2.example.cloud.io\", referrer: \"https://platform.example.com/\"",
	}

	// T3: nginx connect() failed Connection refused (2 lines)
	t3Lines = []string{
		"1779693445803\t2026-05-25T07:17:25.803Z\t2026/05/25 07:17:25 [error] 12898#12898: *188709747 connect() failed (111: Connection refused) while connecting to upstream, client: 10.0.1.0, server: host3.example.cloud.io, request: \"PUT /obj-c/appx/data/groups/%7B11111111-2222-3333-4444-555555555555%7D/%7B22222222-3333-4444-5555-666666666666%7D/blobs/data/%7B33333333-4444-5555-6666-777777777777%7D/%7B44444444-5555-6666-7777-888888888888%7D/file_a?retention&versionId=55555555-6666-7777-8888-999999999999 HTTP/1.1\", upstream: \"http://10.0.0.163:443/obj-c/appx/data/groups/%7B11111111-2222-3333-4444-555555555555%7D/%7B22222222-3333-4444-5555-666666666666%7D/blobs/data/%7B33333333-4444-5555-6666-777777777777%7D/%7B44444444-5555-6666-7777-888888888888%7D/file_a?retention&versionId=55555555-6666-7777-8888-999999999999\", host: \"host3.example.cloud.io\"",
		"1779693445675\t2026-05-25T07:17:25.675Z\t2026/05/25 07:17:25 [error] 13821#13821: *188646924 connect() failed (111: Connection refused) while connecting to upstream, client: 10.0.1.0, server: host3.example.cloud.io, request: \"PUT /obj-d/appx/data/groups/%7Baaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee%7D/%7Bbbbbbbbb-cccc-dddd-eeee-ffffffffffff%7D/blobs/data/%7Bcccccccc-dddd-eeee-ffff-000000000000%7D/%7Bdddddddd-eeee-ffff-0000-111111111111%7D/file_b HTTP/1.1\", upstream: \"http://10.0.0.163:443/obj-d/appx/data/groups/%7Baaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee%7D/%7Bbbbbbbbb-cccc-dddd-eeee-ffffffffffff%7D/blobs/data/%7Bcccccccc-dddd-eeee-ffff-000000000000%7D/%7Bdddddddd-eeee-ffff-0000-111111111111%7D/file_b\", host: \"host3.example.cloud.io\"",
	}

	// T4: nginx JSON access log with status 503 (4 lines)
	t4Lines = []string{
		"1779695999397\t2026-05-25T07:59:59.397Z\t" + buildAccessLogJSON("74924613", "38", "2834", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "90794", "10.0.0.10", "11306", "2026:07:59:59", "07:59:59", "PUT", "/obj-x/appx/data/groups/%7Baaaaaaaa-1111-2222-3333-444444444444%7D/file_x HTTP/1.1", "/obj-x/appx/data/groups/%7Baaaaaaaa-1111-2222-3333-444444444444%7D/file_x", "host4.example.com", "60.069", "60.069", "60.069", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ABcdEFghIJklMNopQRstUVwxYZ012345", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		"1779695994375\t2026-05-25T07:59:54.375Z\t" + buildAccessLogJSON("82950302", "58", "2736", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "527115", "10.0.0.10", "10877", "2026:07:59:54", "07:59:54", "PUT", "/obj-x/appx/data/groups/%7Baaaaaaaa-1111-2222-3333-444444444444%7D/file_y HTTP/1.1", "/obj-x/appx/data/groups/%7Baaaaaaaa-1111-2222-3333-444444444444%7D/file_y", "host4.example.com", "60.124", "60.124", "60.124", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "ABcdEFghIJklMNopQRstUVwxYZ543210", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		"1779695993855\t2026-05-25T07:59:53.855Z\t" + buildAccessLogJSON("81012207", "60", "2735", "cccccccccccccccccccccccccccccccc", "108858", "10.0.1.10", "29789", "2026:07:59:53", "07:59:53", "PUT", "/obj-y/appx/data/key/groups/%7Bbbbbbbbb-2222-3333-4444-555555555555%7D/file_z HTTP/1.1", "/obj-y/appx/data/key/groups/%7Bbbbbbbbb-2222-3333-4444-555555555555%7D/file_z", "host4.example.com", "60.393", "60.093", "60.093", "cccccccccccccccccccccccccccccccc", "ABcdEFghIJklMNopQRstUVwxYZabcdef", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
		"1779695991418\t2026-05-25T07:59:51.418Z\t" + buildAccessLogJSON("92460096", "340", "2934", "dddddddddddddddddddddddddddddddd", "1429", "10.0.0.10", "14800", "2026:07:59:51", "07:59:51", "PUT", "/obj-x/appx/data/groups/%7Baaaaaaaa-1111-2222-3333-444444444444%7D/file_w HTTP/1.1", "/obj-x/appx/data/groups/%7Baaaaaaaa-1111-2222-3333-444444444444%7D/file_w", "host4.example.com", "60.068", "60.068", "60.068", "dddddddddddddddddddddddddddddddd", "ABcdEFghIJklMNopQRstUVwxYZ001122", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
	}

	// T5: nginx [crit] SSL_do_handshake() failed (1 line)
	t5Lines = []string{
		"1779695704223\t2026-05-25T07:55:04.223Z\t2026/05/25 07:55:04 [crit] 2565#2565: *74346384 SSL_do_handshake() failed (SSL: error:0A000119:SSL routines::decryption failed or bad record mac error:0A000139:SSL routines::record layer failure) while SSL handshaking, client: 10.0.0.27, server: 0.0.0.0:443",
	}

	// T6: nginx [crit] SSL_read() failed (1 line)
	t6Lines = []string{
		"1779695539741\t2026-05-25T07:52:19.741Z\t2026/05/25 07:52:19 [crit] 2469#2469: *81458127 SSL_read() failed (SSL: error:0A0000C6:SSL routines::packet length too long error:0A000139:SSL routines::record layer failure) while waiting for request, client: 10.0.0.24, server: 0.0.0.0:443",
	}

	// T7: ingress controller.go SSL certificate error (1 line)
	t7Lines = []string{
		"1779695769581\t2026-05-25T07:56:09.581Z\tW0525 07:56:09.577072       7 controller.go:1455] Error getting SSL certificate \"namespaceA/cert-trial\": local SSL certificate namespaceA/cert-trial was not found. Using default certificate",
	}

	// T8: ingress backend_ssl.go X.509 certificate error (1 line)
	t8Lines = []string{
		"1779695769554\t2026-05-25T07:56:09.554Z\tW0525 07:56:09.553424       7 backend_ssl.go:47] Error obtaining X.509 certificate: no object matching key \"namespaceA/cert-trial\" in local store",
	}
)

// buildAccessLogJSON returns one nginx-access-log-style JSON line with the
// indicated dynamic values plugged in. Keeping this in a helper guarantees
// that the four T4 fixtures have an identical structural template, so we
// test the masking — not test data drift.
func buildAccessLogJSON(connection, connReqs, pid, reqID, reqLen, remoteAddr, remotePort, timeLocal, timeIso, method, request, requestURI, host, reqTime, upHeaderTime, upRespTime, msec, accessKey, signature string) string {
	return `{"msec":"` + msec + `.000","connection":"` + connection + `","connection_requests":"` + connReqs + `","pid":"` + pid + `","request_id":"` + reqID + `","request_length":"` + reqLen + `","remote_addr":"` + remoteAddr + `","remote_user":"","remote_port":"` + remotePort + `","time_local":"25/May/` + timeLocal + ` +0000","time_iso8601":"2026-05-25T` + timeIso + `+00:00","request":"` + method + ` ` + request + `","request_uri":"` + requestURI + `","args":"","status":"503","body_bytes_sent":"126","bytes_sent":"558","http_referer":"","http_user_agent":"APN/1.0 appx/1.0 client/13.0","http_x_forwarded_for":"","http_host":"` + host + `","server_name":"` + host + `","request_time":"` + reqTime + `","upstream":"10.0.0.99:443","upstream_connect_time":"0.000","upstream_header_time":"` + upHeaderTime + `","upstream_response_time":"` + upRespTime + `","upstream_response_length":"126","upstream_proxy_status":"","ssl_protocol":"TLSv1.2","ssl_cipher":"ECDHE-RSA-AES128-GCM-SHA256","scheme":"https","request_method":"` + method + `","server_protocol":"HTTP/1.1","pipe":".","gzip_ratio":"","req_id":"` + reqID + `","auth":"AWS4-HMAC-SHA256 Credential=` + accessKey + `/20260525/eu-west-1/s3/aws4_request,SignedHeaders=content-length;content-type;host;user-agent;x-amz-checksum-sha256;x-amz-content-sha256;x-amz-date;x-amz-sdk-checksum-algorithm;x-amz-storage-class,Signature=` + signature + `","copy_source":"","finalize":"","error_code":"","upstream_error_code":"0104"}`
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// findCluster returns the pattern containing the given input-index, or nil
// if the index is missing (which would indicate the algorithm dropped or
// duplicated a line).
func findCluster(pats []Pattern, lineIdx int) *Pattern {
	for i := range pats {
		if slices.Contains(pats[i].LineIndices, lineIdx) {
			return &pats[i]
		}
	}
	return nil
}

// assertCluster checks that all expectedIndices land in the same cluster, and
// that the cluster has exactly that many members. Reports the actual cluster
// boundaries on failure so the test output points at what split or merged.
func assertCluster(t *testing.T, pats []Pattern, name string, expectedIndices []int) {
	t.Helper()
	if len(expectedIndices) == 0 {
		return
	}
	first := findCluster(pats, expectedIndices[0])
	if first == nil {
		t.Errorf("%s: line %d not present in any cluster", name, expectedIndices[0])
		return
	}
	if len(first.LineIndices) != len(expectedIndices) {
		t.Errorf("%s: expected cluster of size %d, got %d (members=%v, template=%q)", name, len(expectedIndices), len(first.LineIndices), first.LineIndices, first.Template)
		return
	}
	for _, idx := range expectedIndices[1:] {
		c := findCluster(pats, idx)
		if c == nil {
			t.Errorf("%s: line %d not present in any cluster", name, idx)
			continue
		}
		if c.SkeletonKey != first.SkeletonKey {
			t.Errorf("%s: line %d expected in same cluster as line %d, but skeletons differ:\n  got:  %q\n  want: %q", name, idx, expectedIndices[0], c.SkeletonKey, first.SkeletonKey)
		}
	}
}

// TestRoutingPolicyClusters: three lines that only differ in tenant_id and
// time should all collapse to one pattern.
func TestRoutingPolicyClusters(t *testing.T) {
	pats := ExtractPatterns(routingPolicyLines)
	if len(pats) != 1 {
		t.Fatalf("expected 1 cluster, got %d:\n%s", len(pats), dumpPatterns(pats))
	}
	if len(pats[0].LineIndices) != 3 {
		t.Fatalf("expected cluster of size 3, got %d", len(pats[0].LineIndices))
	}
	if !strings.Contains(pats[0].Template, "determined upload destination based on routing policy") {
		t.Errorf("template should keep the literal message; got: %q", pats[0].Template)
	}
	if !strings.Contains(pats[0].Template, "*") {
		t.Errorf("template should contain at least one *; got: %q", pats[0].Template)
	}
}

// TestRouteResolvedDistinctFromRoutingPolicy: route-resolved and routing-policy
// share a pod prefix but the trailing message structure differs, so they must
// end up in separate clusters.
func TestRouteResolvedDistinctFromRoutingPolicy(t *testing.T) {
	mixed := append([]string{}, routingPolicyLines...)
	mixed = append(mixed, routeResolvedLines...)
	pats := ExtractPatterns(mixed)
	if len(pats) != 2 {
		t.Fatalf("expected 2 clusters, got %d:\n%s", len(pats), dumpPatterns(pats))
	}
	assertCluster(t, pats, "routing-policy", []int{0, 1, 2})
	assertCluster(t, pats, "route-resolved", []int{3, 4})
}

// TestPutRequestsCluster: PUT requests with different paths and request IDs
// should land in the same cluster.
func TestPutRequestsCluster(t *testing.T) {
	pats := ExtractPatterns(putRequestLines)
	if len(pats) != 1 {
		t.Fatalf("expected 1 cluster, got %d:\n%s", len(pats), dumpPatterns(pats))
	}
}

// TestFourteenLineSample: feed the 14-line nginx sample (anonymized) and
// verify the 8 templates predicted in chat (T1..T8 with counts 1/3/2/4/1/1/1/1).
func TestFourteenLineSample(t *testing.T) {
	var all []string
	// Order: keep T2 before T1 so we test sort-by-count (T2=3 should appear before T1=1).
	// Index ranges:
	//   t1Lines: [0]
	//   t2Lines: [1..3]
	//   t3Lines: [4..5]
	//   t4Lines: [6..9]
	//   t5Lines: [10]
	//   t6Lines: [11]
	//   t7Lines: [12]
	//   t8Lines: [13]
	all = append(all, t1Lines...)
	all = append(all, t2Lines...)
	all = append(all, t3Lines...)
	all = append(all, t4Lines...)
	all = append(all, t5Lines...)
	all = append(all, t6Lines...)
	all = append(all, t7Lines...)
	all = append(all, t8Lines...)

	pats := ExtractPatterns(all)
	if len(pats) != 8 {
		t.Fatalf("expected 8 clusters, got %d:\n%s", len(pats), dumpPatterns(pats))
	}

	assertCluster(t, pats, "T1 upstream-connecting", []int{0})
	assertCluster(t, pats, "T2 upstream-reading", []int{1, 2, 3})
	assertCluster(t, pats, "T3 connect-failed", []int{4, 5})
	assertCluster(t, pats, "T4 access-log-json", []int{6, 7, 8, 9})
	assertCluster(t, pats, "T5 ssl-handshake", []int{10})
	assertCluster(t, pats, "T6 ssl-read", []int{11})
	assertCluster(t, pats, "T7 ssl-cert", []int{12})
	assertCluster(t, pats, "T8 x509-cert", []int{13})

	// Sort order: T4 (4) → T2 (3) → T3 (2) → singletons in original order.
	if got := len(pats[0].LineIndices); got != 4 {
		t.Errorf("top cluster should be the access-log-json group (size 4); got size %d, template=%q", got, pats[0].Template)
	}
	if got := len(pats[1].LineIndices); got != 3 {
		t.Errorf("second cluster should be the upstream-reading group (size 3); got size %d, template=%q", got, pats[1].Template)
	}
	if got := len(pats[2].LineIndices); got != 2 {
		t.Errorf("third cluster should be the connect-failed group (size 2); got size %d, template=%q", got, pats[2].Template)
	}
}

// TestEmptyInput: degenerate input shouldn't panic and should return nil.
func TestEmptyInput(t *testing.T) {
	if pats := ExtractPatterns(nil); pats != nil {
		t.Errorf("expected nil for nil input, got %v", pats)
	}
	if pats := ExtractPatterns([]string{}); pats != nil {
		t.Errorf("expected nil for empty input, got %v", pats)
	}
}

// TestSingletonsKeepOrder: ties broken by first-occurrence index, so a
// stream of unique singletons preserves input order.
func TestSingletonsKeepOrder(t *testing.T) {
	in := []string{
		"first unique singleton alpha",
		"second unique singleton beta",
		"third unique singleton gamma",
	}
	pats := ExtractPatterns(in)
	if len(pats) != 3 {
		t.Fatalf("expected 3 singleton clusters, got %d", len(pats))
	}
	for i, p := range pats {
		if len(p.LineIndices) != 1 || p.LineIndices[0] != i {
			t.Errorf("cluster %d should point at input line %d; got indices=%v", i, i, p.LineIndices)
		}
	}
}

// TestCollapseConsecutiveStars: two adjacent dynamic tokens collapse to a
// single "*" in the displayed Template but the SkeletonKey keeps them apart
// so two-vs-three masked tokens in a row still cluster separately.
func TestCollapseConsecutiveStars(t *testing.T) {
	in := []string{
		"x 1234 5678 y",
		"x 9999 8888 y",
	}
	pats := ExtractPatterns(in)
	if len(pats) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(pats))
	}
	if got := pats[0].Template; got != "x * y" {
		t.Errorf("expected collapsed template %q, got %q", "x * y", got)
	}
	if got := pats[0].SkeletonKey; got != "x * * y" {
		t.Errorf("expected uncollapsed key %q, got %q", "x * * y", got)
	}
}

// dumpPatterns is a debug helper used in failure messages so a failing
// cluster count points the developer at which lines went where.
func dumpPatterns(pats []Pattern) string {
	var b strings.Builder
	for i, p := range pats {
		b.WriteString("  [")
		b.WriteString(itoa(i))
		b.WriteString("] (")
		b.WriteString(itoa(len(p.LineIndices)))
		b.WriteString(") indices=")
		for j, idx := range p.LineIndices {
			if j > 0 {
				b.WriteString(",")
			}
			b.WriteString(itoa(idx))
		}
		b.WriteString(" template=")
		b.WriteString(p.Template)
		b.WriteString("\n")
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
