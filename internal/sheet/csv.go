package sheet

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// bom é o marcador UTF-8. Sem ele o Excel abre o arquivo em ANSI e os acentos
// dos cabeçalhos e das descrições do PagBank saem corrompidos.
const bom = "\uFEFF"

// Options controla a apresentação do CSV. O zero value não é utilizável;
// use DefaultOptions.
type Options struct {
	// Comma separa os campos. O Excel pt-BR espera ';', porque a vírgula já é o
	// separador decimal.
	Comma rune
	// BOM escreve o marcador UTF-8 no início do arquivo.
	BOM bool
	// DecimalComma apresenta números com vírgula decimal.
	DecimalComma bool
	// CRLF termina as linhas com \r\n.
	CRLF bool
}

// DefaultOptions produz um CSV que o Excel em português abre com um duplo clique.
func DefaultOptions() Options {
	return Options{Comma: ';', BOM: true, DecimalComma: true, CRLF: true}
}

// Write grava a tabela — cabeçalho e linhas — no writer.
func Write(w io.Writer, t Table, opt Options) error {
	if opt.BOM {
		if _, err := io.WriteString(w, bom); err != nil {
			return err
		}
	}
	cw := csv.NewWriter(w)
	cw.Comma = opt.Comma
	cw.UseCRLF = opt.CRLF

	header := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		header[i] = c.Header
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	cells := make([]string, len(t.Columns))
	for _, row := range t.Rows {
		for i := range t.Columns {
			v := ""
			if i < len(row) {
				v = row[i]
			}
			cells[i] = format(v, t.Columns[i].Kind, opt)
		}
		if err := cw.Write(cells); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteFile grava a tabela em dir/<nome>.csv e devolve o caminho do arquivo.
func WriteFile(dir string, t Table, opt Options) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName(t.Name))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if err := Write(f, t, opt); err != nil {
		f.Close()
		return "", fmt.Errorf("gravando %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("fechando %s: %w", path, err)
	}
	return path, nil
}

// invalidFileNameChars são os caracteres que o Windows não aceita em nome de
// arquivo, mais o espaço, que só atrapalha na linha de comando.
const invalidFileNameChars = `/\:*?"<>| `

// FileName transforma o nome de uma tabela em nome de arquivo seguro.
func FileName(name string) string {
	safe := strings.Map(func(r rune) rune {
		if strings.ContainsRune(invalidFileNameChars, r) {
			return '_'
		}
		return r
	}, name)
	if safe == "" {
		safe = "tabela"
	}
	return safe + ".csv"
}

func format(v string, kind Kind, opt Options) string {
	if v == "" {
		return ""
	}
	switch kind {
	case KindNumber:
		if opt.DecimalComma {
			if n, ok := decimalComma(v); ok {
				return n
			}
		}
		return v
	case KindDate:
		if t, ok := parseTime(v); ok {
			return t.Format("02/01/2006")
		}
		return v
	case KindDateTime:
		if t, ok := parseTime(v); ok {
			return t.Format("02/01/2006 15:04:05")
		}
		return v
	default:
		return escapeFormula(v)
	}
}

// decimalComma troca o ponto decimal por vírgula, sem passar por float — o valor
// vem do PagBank em decimal exato e reformatá-lo introduziria erro de
// arredondamento em centavos. Devolve false se a string não for um número.
func decimalComma(v string) (string, bool) {
	s := v
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return "", false
	}
	dot := -1
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] >= '0' && s[i] <= '9':
		case s[i] == '.' && dot < 0:
			dot = i
		default:
			return "", false
		}
	}
	if dot < 0 {
		return v, true
	}
	return strings.Replace(v, ".", ",", 1), true
}

// layouts aceitos nas células canônicas de data. O primeiro que casar vence.
var layouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseTime(v string) (time.Time, bool) {
	for _, l := range layouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// escapeFormula neutraliza células de texto que o Excel interpretaria como
// fórmula. Um '-' inicial fica intocado: é sinal de valor negativo em campos
// textuais do extrato, e prefixá-lo atrapalharia mais do que protege.
func escapeFormula(v string) string {
	switch v[0] {
	case '=', '+', '@', '\t', '\r':
		return "'" + v
	}
	return v
}
