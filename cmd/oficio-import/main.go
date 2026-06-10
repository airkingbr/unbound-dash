// Command oficio-import le um oficio de bloqueio de DNS em PDF, extrai os
// dominios e metadados (origem, fonte, data) usando os templates do
// pacote internal/oficio e, opcionalmente, envia os dominios para a API
// de blocklist de uma instancia do unbound-dash.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strings"

	"github.com/airkingbr/unbound-dash/internal/oficio"
)

func main() {
	apply := flag.Bool("apply", false, "envia os dominios extraidos para a API do unbound-dash")
	url := flag.String("url", "", "URL base do dashboard (ex: https://meuservidor:8080)")
	password := flag.String("password", "", "senha de admin do dashboard")
	insecure := flag.Bool("insecure", false, "ignora verificacao de certificado TLS")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "uso: oficio-import [-apply -url URL -password SENHA] <arquivo.pdf>")
		os.Exit(1)
	}

	text, err := extractText(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro ao ler PDF:", err)
		os.Exit(1)
	}

	result, err := oficio.Parse(text)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro ao processar oficio:", err)
		os.Exit(1)
	}

	fmt.Printf("origem: %s\n", result.Origem)
	fmt.Printf("fonte:  %s\n", result.Fonte)
	fmt.Printf("data:   %s\n", result.Data)
	fmt.Printf("dominios encontrados: %d\n", len(result.Domains))
	for _, d := range result.Domains {
		fmt.Println("  " + d)
	}

	if !*apply {
		return
	}

	if *url == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "-url e -password sao obrigatorios com -apply")
		os.Exit(1)
	}

	if err := applyBlocklist(*url, *password, *insecure, result); err != nil {
		fmt.Fprintln(os.Stderr, "erro ao aplicar bloqueios:", err)
		os.Exit(1)
	}
}

func extractText(pdfPath string) (string, error) {
	cmd := exec.Command("pdftotext", "-enc", "UTF-8", "-layout", pdfPath, "-")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func applyBlocklist(baseURL, password string, insecure bool, result oficio.Result) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client := &http.Client{Jar: jar}
	if insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}

	baseURL = strings.TrimSuffix(baseURL, "/")

	loginBody, _ := json.Marshal(map[string]string{"password": password})
	resp, err := client.Post(baseURL+"/api/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login falhou: status %d", resp.StatusCode)
	}

	for _, domain := range result.Domains {
		body, _ := json.Marshal(map[string]string{
			"domain": domain,
			"origem": result.Origem,
			"fonte":  result.Fonte,
			"data":   result.Data,
		})
		resp, err := client.Post(baseURL+"/api/blocklist", "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("%s: %w", domain, err)
		}
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			fmt.Printf("ok:        %s\n", domain)
		case http.StatusConflict:
			fmt.Printf("ja existe: %s\n", domain)
		default:
			fmt.Printf("erro (%d):  %s -> %s\n", resp.StatusCode, domain, strings.TrimSpace(string(msg)))
		}
	}
	return nil
}
