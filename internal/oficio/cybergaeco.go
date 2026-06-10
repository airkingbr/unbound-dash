package oficio

import (
	"fmt"
	"regexp"
	"strings"
)

func init() {
	Register(cyberGaeco{})
}

// cyberGaeco processa oficios do CyberGaeco/MPSP (Nucleo de Investigacao
// de Crimes Ciberneticos), enviados como uma carta seguida de um anexo em
// formato de tabela com as colunas: marca pirata, dominios ja bloqueados
// e novos dominios para bloqueio.
type cyberGaeco struct{}

func (cyberGaeco) Name() string { return "cybergaeco" }

func (cyberGaeco) Detect(text string) bool {
	return strings.Contains(text, "CyberGaeco")
}

var (
	cgOficioRE = regexp.MustCompile(`Of[íi]cio\s+n[ºo°]?\s*(\d+)`)
	cgSEIRE    = regexp.MustCompile(`SEI\s+([\d.\-]+)`)
	cgDataRE   = regexp.MustCompile(`(\d{1,2}) de (\w+) de (\d{4})`)
	cgDomainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

	// Linhas de cabecalho/rodape repetidas em cada pagina do anexo, que
	// devem ser ignoradas ao montar a lista de dominios.
	cgNoiseRE = regexp.MustCompile(`Rge-CYBERGAECO|Planilha|/ pg\.|CYBERGAECO - INVESTIGA|BERN[ÉE]TICOS|MARCA PIRATA|DOM[ÍI]NIOS J[ÁA] BLOQUEADOS|NOVOS DOM[ÍI]NIOS|PARA BLOQUEIO`)

	cgMonths = map[string]string{
		"janeiro": "01", "fevereiro": "02", "março": "03", "marco": "03",
		"abril": "04", "maio": "05", "junho": "06", "julho": "07",
		"agosto": "08", "setembro": "09", "outubro": "10",
		"novembro": "11", "dezembro": "12",
	}
)

func (cyberGaeco) Parse(text string) (Result, error) {
	res := Result{Origem: "CyberGaeco/MPSP"}

	if m := cgOficioRE.FindStringSubmatch(text); m != nil {
		res.Fonte = "Oficio " + m[1]
	}
	if m := cgSEIRE.FindStringSubmatch(text); m != nil {
		if res.Fonte != "" {
			res.Fonte += " SEI " + m[1]
		} else {
			res.Fonte = "SEI " + m[1]
		}
	}
	if m := cgDataRE.FindStringSubmatch(text); m != nil {
		day := m[1]
		if len(day) == 1 {
			day = "0" + day
		}
		if month, ok := cgMonths[strings.ToLower(m[2])]; ok {
			res.Data = fmt.Sprintf("%s-%s-%s", m[3], month, day)
		}
	}

	domains, err := cgParseAnnex(text)
	if err != nil {
		return res, err
	}
	res.Domains = domains
	return res, nil
}

// cgParseAnnex extrai os dominios da tabela do anexo. As linhas seguem o
// padrao "marca   dominio1   [dominio2]"; alguns dominios sao quebrados
// em duas linhas pelo PDF (ficando apenas o sufixo na linha seguinte,
// fortemente indentada), por isso o estado "pending" junta esses casos.
func cgParseAnnex(text string) ([]string, error) {
	idx := strings.Index(text, "Anexo")
	if idx < 0 {
		return nil, fmt.Errorf("secao 'Anexo' nao encontrada no documento")
	}

	seen := map[string]bool{}
	var domains []string
	pending := ""

	add := func(s string) {
		d := strings.SplitN(s, "/", 2)[0]
		if cgDomainRE.MatchString(d) && !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}

	for _, line := range strings.Split(text[idx:], "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || cgNoiseRE.MatchString(trimmed) {
			pending = ""
			continue
		}

		fields := strings.Fields(trimmed)
		isContinuation := len(fields) == 1 && strings.HasPrefix(line, " ")

		if isContinuation {
			if pending != "" {
				candidate := pending + fields[0]
				if cgDomainRE.MatchString(candidate) {
					add(candidate)
				}
				pending = ""
			}
			continue
		}

		// fields[0] e a coluna "marca pirata"; o restante sao os
		// dominios das colunas seguintes.
		var leftover []string
		for _, f := range fields[1:] {
			d := strings.SplitN(f, "/", 2)[0]
			if cgDomainRE.MatchString(d) {
				add(f)
			} else {
				leftover = append(leftover, f)
			}
		}
		pending = strings.Join(leftover, "")
	}

	return domains, nil
}
