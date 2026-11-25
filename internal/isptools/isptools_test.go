package isptools

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeAPI(t *testing.T, list string, status int) *httptest.Server {
	t.Helper()
	sum := md5.Sum([]byte(list))
	m := hex.EncodeToString(sum[:])
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ip" && r.Header.Get("Authorization") != "Bearer isptbl_test" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"erro":"token invalido"}`)
			return
		}
		if strings.Contains(r.URL.RawQuery, "token=") {
			t.Errorf("token sent in URL")
		}
		if status != 200 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"erro":"falhou"}`)
			return
		}
		switch r.URL.Path {
		case "/versao":
			fmt.Fprintf(w, `{"atualizado_em":"2026-11-25 11:29:32 -0300","versao":7,"dominios":3,"md5":"x","md5_formatos":{"texto":"%s"},"excecoes_cliente":true}`, m)
		case "/lista":
			w.Header().Set("X-Blocklist-MD5", m)
			fmt.Fprint(w, list)
		case "/ip":
			fmt.Fprint(w, "203.0.113.9\n")
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestClientVersaoAndLista(t *testing.T) {
	list := "a.com\nb.net\n"
	srv := fakeAPI(t, list, 200)
	defer srv.Close()
	c := NewClient(srv.URL, "isptbl_test", "test")

	v, err := c.Versao(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Versao != 7 || !v.ExcecoesCliente || v.MD5Texto() == "" {
		t.Fatalf("unexpected versao: %+v", v)
	}
	body, err := c.Lista(context.Background(), v.MD5Texto())
	if err != nil || string(body) != list {
		t.Fatalf("lista: %q %v", body, err)
	}
	if _, err := c.Lista(context.Background(), "00000000000000000000000000000000"); err == nil {
		t.Fatal("expected md5 mismatch error")
	}
}

func TestClientErrors(t *testing.T) {
	srv := fakeAPI(t, "", 429)
	defer srv.Close()
	_, err := NewClient(srv.URL, "isptbl_test", "").Versao(context.Background())
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 429 || apiErr.RetryAfter != time.Hour || apiErr.Message != "falhou" {
		t.Fatalf("unexpected error: %#v", err)
	}

	_, err = NewClient(srv.URL, "wrong", "").Versao(context.Background())
	if StatusOf(err) != 401 {
		t.Fatalf("expected 401, got %v", err)
	}
}

func TestSyncRejectsSmallList(t *testing.T) {
	srv := fakeAPI(t, "a.com\nb.net\n", 200)
	defer srv.Close()
	dir := t.TempDir()
	s := NewSyncer(Options{Client: NewClient(srv.URL, "isptbl_test", ""), StateDir: dir, BlocklistFile: filepath.Join(dir, "bl.conf")})
	_, err := s.Sync(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "lista suspeita") {
		t.Fatalf("expected small-list rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "bl.conf")); !os.IsNotExist(statErr) {
		t.Fatal("persistent file must not be written for a rejected list")
	}
	if st := s.Status(); st.State != "erro" || st.Dominios != 0 {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestLookupAndSearch(t *testing.T) {
	s := &Syncer{}
	s.setDomains([]string{"bet.com", "x.casino.net", "casino.net"})
	cases := map[string]string{
		"bet.com":          "bet.com",
		"www.BET.com.":     "bet.com",
		"a.b.x.casino.net": "casino.net",
		"notbet.com":       "",
		"com":              "",
	}
	for in, want := range cases {
		if got := s.Lookup(in); got != want {
			t.Errorf("Lookup(%q) = %q, want %q", in, got, want)
		}
	}
	items, total := s.Search("casino", 1)
	if total != 2 || len(items) != 1 {
		t.Fatalf("Search: %v %d", items, total)
	}
}

func TestReadZoneFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "anatel.conf")
	content := "server:\n    local-zone: \"Foo.com.\" always_nxdomain  # origem=CyberGaeco/MPSP data=2026-01-01\nlocal-zone: \"bar.net\" always_nxdomain\nlocal-zone: \"keep.org.\" static\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readZoneFile(p)
	if err != nil || strings.Join(got, ",") != "foo.com,bar.net" {
		t.Fatalf("readZoneFile = %v, %v", got, err)
	}
}

func TestAPIDomainRE(t *testing.T) {
	for _, ok := range []string{"0-1111bet.com", "_dmarc.x.com", "a.b"} {
		if !apiDomainRE.MatchString(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"com", "-a.com", "a..com", "a.com\"", "a.com always_nxdomain", "A.COM"} {
		if apiDomainRE.MatchString(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
