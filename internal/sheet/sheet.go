// Package sheet modela os dados extraídos como tabelas genéricas — colunas
// ordenadas e linhas de células — e as grava em planilha.
//
// As fontes do PagBank devolvem formatos incompatíveis entre si (JSON do EDI,
// XML da API legada) e o layout do EDI v3.00 não é publicado. Em vez de forçar
// um schema comum, cada fonte monta suas próprias tabelas e nada dos campos de
// origem é descartado.
//
// As células guardam valores em forma canônica, independente de localidade:
//
//	KindText      texto livre, gravado como veio
//	KindDate      "2006-01-02"
//	KindDateTime  "2006-01-02T15:04:05" (sufixo de fuso opcional)
//	KindNumber    "-1234.56" — ponto decimal, sem separador de milhar
//
// A conversão para a apresentação pt-BR acontece só na hora de gravar, em csv.go.
package sheet

import "fmt"

// Kind é o tipo de uma coluna, que decide como as células são apresentadas.
type Kind int

const (
	KindText Kind = iota
	KindDate
	KindDateTime
	KindNumber
)

func (k Kind) String() string {
	switch k {
	case KindDate:
		return "date"
	case KindDateTime:
		return "datetime"
	case KindNumber:
		return "number"
	default:
		return "text"
	}
}

// Column é o cabeçalho de uma coluna e o tipo das células abaixo dele.
type Column struct {
	Header string
	Kind   Kind
}

// Table é um conjunto de registros de um mesmo tipo — vira um arquivo na saída.
// Toda linha tem exatamente len(Columns) células.
type Table struct {
	Name    string
	Columns []Column
	Rows    [][]string
}

// Empty diz se a tabela não tem nenhum registro.
func (t Table) Empty() bool { return len(t.Rows) == 0 }

// Builder monta uma Table permitindo que colunas apareçam a qualquer momento.
//
// Isso existe por causa do EDI: como o layout não é documentado, as colunas são
// descobertas a partir do próprio payload, e um campo pode surgir só na segunda
// página. Linhas gravadas antes disso são preenchidas com célula vazia.
type Builder struct {
	name    string
	columns []Column
	index   map[string]int
	rows    []map[int]string
	current map[int]string
}

// NewBuilder cria um Builder para uma tabela chamada name.
func NewBuilder(name string) *Builder {
	return &Builder{name: name, index: make(map[string]int)}
}

// Column registra uma coluna e devolve sua posição, criando-a se for a primeira
// vez que aparece. Quando o mesmo cabeçalho chega com tipos diferentes a coluna
// é rebaixada para KindText, que é o único tipo que apresenta qualquer valor sem
// distorcer.
func (b *Builder) Column(header string, kind Kind) int {
	if i, ok := b.index[header]; ok {
		if b.columns[i].Kind != kind {
			b.columns[i].Kind = KindText
		}
		return i
	}
	i := len(b.columns)
	b.columns = append(b.columns, Column{Header: header, Kind: kind})
	b.index[header] = i
	return i
}

// StartRow abre uma nova linha. A linha anterior, se houver, é fechada.
func (b *Builder) StartRow() {
	b.closeRow()
	b.current = make(map[int]string)
}

// Set grava uma célula na linha aberta, criando a coluna se necessário.
// Chamar Set sem uma linha aberta é erro de programação e entra em panic.
func (b *Builder) Set(header string, kind Kind, value string) {
	if b.current == nil {
		panic(fmt.Sprintf("sheet: Set(%q) sem StartRow", header))
	}
	b.current[b.Column(header, kind)] = value
}

func (b *Builder) closeRow() {
	if b.current != nil {
		b.rows = append(b.rows, b.current)
		b.current = nil
	}
}

// Len é a quantidade de linhas já acumuladas, incluindo a que estiver aberta.
func (b *Builder) Len() int {
	n := len(b.rows)
	if b.current != nil {
		n++
	}
	return n
}

// Build fecha a linha aberta e materializa a tabela, alinhando todas as linhas
// ao conjunto final de colunas.
func (b *Builder) Build() Table {
	b.closeRow()
	t := Table{Name: b.name, Columns: b.columns, Rows: make([][]string, 0, len(b.rows))}
	for _, sparse := range b.rows {
		row := make([]string, len(b.columns))
		for i, v := range sparse {
			row[i] = v
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}
