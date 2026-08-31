package edi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RangelReale/pagbank/internal/sheet"
)

// O layout do JSON do EDI v3.00 não é publicado, então este arquivo trata a
// resposta como um documento genérico: descobre onde está a lista de registros,
// achata cada um em pares chave/valor e deixa o pacote sheet montar as colunas
// na ordem em que os campos apareceram. Um campo novo do PagBank vira uma coluna
// nova sem exigir mudança de código.

type nodeKind int

const (
	kindNull nodeKind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// node é um valor JSON que lembra a ordem das chaves — um map do Go não lembra,
// e a ordem do documento é a única pista de ordenação de colunas que existe.
type node struct {
	kind nodeKind
	keys []string // ordem de aparição, só para objetos
	obj  map[string]*node
	arr  []*node
	num  json.Number
	str  string
	b    bool
}

// parse lê o documento inteiro preservando a ordem das chaves.
func parse(data []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return parseValue(dec, tok)
}

func parseValue(dec *json.Decoder, tok json.Token) (*node, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return parseObject(dec)
		case '[':
			return parseArray(dec)
		}
		return nil, fmt.Errorf("token %q inesperado", t)
	case json.Number:
		return &node{kind: kindNumber, num: t}, nil
	case string:
		return &node{kind: kindString, str: t}, nil
	case bool:
		return &node{kind: kindBool, b: t}, nil
	case nil:
		return &node{kind: kindNull}, nil
	}
	return nil, fmt.Errorf("token %v inesperado", tok)
}

func parseObject(dec *json.Decoder) (*node, error) {
	n := &node{kind: kindObject, obj: map[string]*node{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("chave de objeto não textual: %v", kt)
		}
		vt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		v, err := parseValue(dec, vt)
		if err != nil {
			return nil, err
		}
		if _, existe := n.obj[key]; !existe {
			n.keys = append(n.keys, key)
		}
		n.obj[key] = v
	}
	_, err := dec.Token() // fecha '}'
	return n, err
}

func parseArray(dec *json.Decoder) (*node, error) {
	n := &node{kind: kindArray}
	for dec.More() {
		vt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		v, err := parseValue(dec, vt)
		if err != nil {
			return nil, err
		}
		n.arr = append(n.arr, v)
	}
	_, err := dec.Token() // fecha ']'
	return n, err
}

// get devolve o filho de um objeto.
func (n *node) get(key string) *node {
	if n == nil || n.kind != kindObject {
		return nil
	}
	return n.obj[key]
}

// int lê um valor numérico, aceitando também número em string — as APIs do
// PagBank misturam as duas formas na paginação.
func (n *node) int() (int, bool) {
	if n == nil {
		return 0, false
	}
	switch n.kind {
	case kindNumber:
		v, err := strconv.Atoi(n.num.String())
		return v, err == nil
	case kindString:
		v, err := strconv.Atoi(strings.TrimSpace(n.str))
		return v, err == nil
	}
	return 0, false
}

// recordKeys são os nomes que as APIs de extrato do PagBank já usaram para a
// lista de registros. Se nenhum aparecer, vale o primeiro array de objetos.
var recordKeys = []string{"detalhes", "movimentos", "content", "data", "items", "itens", "records", "registros", "results", "elements"}

// records localiza a lista de registros na resposta. Devolve também o nome da
// chave onde ela estava, que entra nas mensagens de diagnóstico.
func records(root *node) ([]*node, string) {
	if root == nil {
		return nil, ""
	}
	// A resposta pode ser o array direto.
	if root.kind == kindArray {
		return root.arr, ""
	}
	if root.kind != kindObject {
		return nil, ""
	}
	for _, k := range recordKeys {
		if c := root.get(k); c != nil && c.kind == kindArray {
			return c.arr, k
		}
	}
	for _, k := range root.keys {
		c := root.obj[k]
		if c.kind == kindArray && len(c.arr) > 0 && c.arr[0].kind == kindObject {
			return c.arr, k
		}
	}
	return nil, ""
}

// pageInfo é o que se conseguiu descobrir sobre a paginação da resposta.
type pageInfo struct {
	TotalPages int
	Known      bool
}

// totalPagesKeys cobre as grafias já vistas nas APIs do PagBank.
var totalPagesKeys = []string{"totalPages", "total_pages", "totalPaginas", "qtdePaginas"}

// pagination procura o total de páginas no corpo, na raiz ou dentro de um
// objeto de paginação. Não achar não é erro: nesse caso a paginação para quando
// uma página vem com menos registros que o pedido.
func pagination(root *node) pageInfo {
	if root == nil || root.kind != kindObject {
		return pageInfo{}
	}
	candidatos := []*node{root}
	for _, k := range []string{"pagination", "paginacao", "page", "pageable"} {
		if c := root.get(k); c != nil && c.kind == kindObject {
			candidatos = append(candidatos, c)
		}
	}
	for _, c := range candidatos {
		for _, k := range totalPagesKeys {
			if v, ok := c.get(k).int(); ok {
				return pageInfo{TotalPages: v, Known: true}
			}
		}
	}
	return pageInfo{}
}

// field é uma célula achatada, pronta para virar coluna.
type field struct {
	Path  string
	Kind  sheet.Kind
	Value string
}

// flatten percorre o registro na ordem do documento e devolve uma célula por
// valor escalar. Objetos aninhados viram "pai.filho"; arrays de objetos ganham
// índice, "chave[1].campo"; arrays de escalares viram um só campo com os valores
// separados por " | ", que numa planilha lê melhor do que dezenas de colunas.
func flatten(n *node) []field {
	var out []field
	walk("", n, &out)
	return out
}

func walk(prefix string, n *node, out *[]field) {
	switch n.kind {
	case kindObject:
		for _, k := range n.keys {
			walk(join(prefix, k), n.obj[k], out)
		}
	case kindArray:
		// Array vazio não vira coluna: ela ficaria vazia em toda linha, e as
		// linhas em que o array tem conteúdo já têm colunas próprias.
		if len(n.arr) == 0 {
			return
		}
		if scalarArray(n) {
			partes := make([]string, len(n.arr))
			for i, item := range n.arr {
				partes[i], _ = scalar(item)
			}
			*out = append(*out, field{Path: prefix, Kind: sheet.KindText, Value: strings.Join(partes, " | ")})
			return
		}
		for i, item := range n.arr {
			walk(fmt.Sprintf("%s[%d]", prefix, i+1), item, out)
		}
	default:
		v, k := scalar(n)
		*out = append(*out, field{Path: prefix, Kind: k, Value: v})
	}
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func scalarArray(n *node) bool {
	for _, item := range n.arr {
		if item.kind == kindObject || item.kind == kindArray {
			return false
		}
	}
	return true
}

// scalar converte um valor folha para a forma canônica do pacote sheet.
func scalar(n *node) (string, sheet.Kind) {
	switch n.kind {
	case kindNumber:
		// O literal do JSON já está em decimal com ponto, que é a forma
		// canônica; passar por float64 arredondaria centavos.
		return n.num.String(), sheet.KindNumber
	case kindBool:
		return strconv.FormatBool(n.b), sheet.KindText
	case kindString:
		return normalizeString(n.str)
	default: // null
		return "", sheet.KindText
	}
}

var (
	reISODate     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reISODateTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`)
	reBRDate      = regexp.MustCompile(`^\d{2}/\d{2}/\d{4}$`)
)

// normalizeString reconhece datas dentro de strings para que o CSV as apresente
// no formato pt-BR. Qualquer outra coisa — inclusive algo que pareça número —
// fica como texto: NSU, BIN e código de autorização são identificadores, e
// tratá-los como número comeria o zero à esquerda.
func normalizeString(s string) (string, sheet.Kind) {
	switch {
	case reISODate.MatchString(s):
		return s, sheet.KindDate
	case reISODateTime.MatchString(s):
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Format("2006-01-02T15:04:05Z07:00"), sheet.KindDateTime
		}
		if t, err := time.Parse("2006-01-02T15:04:05", strings.Replace(s, " ", "T", 1)); err == nil {
			return t.Format("2006-01-02T15:04:05"), sheet.KindDateTime
		}
		return s, sheet.KindText
	case reBRDate.MatchString(s):
		if t, err := time.Parse("02/01/2006", s); err == nil {
			return t.Format("2006-01-02"), sheet.KindDate
		}
	}
	return s, sheet.KindText
}
