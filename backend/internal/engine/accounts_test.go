package engine

import "testing"

func TestSplitProxyNote(t *testing.T) {
	cases := []struct {
		in, url, note string
	}{
		{"socks5://192.168.8.141:54399#%E6%97%A5%E6%9C%ACZ05%20%7C%20%E4%B8%8B%E8%BD%BD%E4%B8%93%E7%94%A8", "socks5://192.168.8.141:54399", "日本Z05 | 下载专用"},
		{"socks5://host:1080 #家宽", "socks5://host:1080", "家宽"},
		{"http://127.0.0.1:7890|备用", "http://127.0.0.1:7890", "备用"},
		{"socks5://plain:1080", "socks5://plain:1080", ""},
	}
	for _, c := range cases {
		u, n := splitProxyNote(c.in)
		if u != c.url || n != c.note {
			t.Fatalf("%q => url=%q note=%q, want url=%q note=%q", c.in, u, n, c.url, c.note)
		}
	}
}
