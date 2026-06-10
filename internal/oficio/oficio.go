// Package oficio extrai dominios e metadados (origem, fonte, data) de
// oficios de bloqueio de DNS recebidos em PDF. Cada orgao/remetente pode
// ter um formato de tabela diferente, entao o pacote registra um Template
// por origem e seleciona o primeiro que reconhecer o texto do documento.
package oficio

import "fmt"

// Result contem os dados extraidos de um oficio, prontos para serem
// gravados como entradas de blocklist.
type Result struct {
	Origem  string   `json:"origem"`
	Fonte   string   `json:"fonte"`
	Data    string   `json:"data"`
	Domains []string `json:"domains"`
}

// Template sabe reconhecer e processar oficios de uma origem especifica.
type Template interface {
	Name() string
	Detect(text string) bool
	Parse(text string) (Result, error)
}

var templates []Template

// Register adiciona um template a lista de templates conhecidos.
func Register(t Template) {
	templates = append(templates, t)
}

// Parse percorre os templates registrados e processa o texto com o
// primeiro que reconhecer o documento.
func Parse(text string) (Result, error) {
	for _, t := range templates {
		if t.Detect(text) {
			return t.Parse(text)
		}
	}
	return Result{}, fmt.Errorf("nenhum template reconhece este documento")
}
