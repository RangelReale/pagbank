package edi

import (
	"testing"

	"github.com/RangelReale/pagbank/internal/sheet"
)

func parseOK(t *testing.T, doc string) *node {
	t.Helper()
	n, err := parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return n
}

func TestFlattenPreservaAOrdemDoDocumento(t *testing.T) {
	// A ordem das colunas da planilha é a ordem do JSON, e um map do Go não a
	// guarda — este é o teste que garante o percurso ordenado.
	n := parseOK(t, `{"z":1,"a":2,"m":{"y":3,"b":4},"c":5}`)
	got := flatten(n)

	want := []string{"z", "a", "m.y", "m.b", "c"}
	if len(got) != len(want) {
		t.Fatalf("campos = %d, quero %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Path != w {
			t.Errorf("campo %d = %q, quero %q", i, got[i].Path, w)
		}
	}
}

func TestFlattenTiposEscalares(t *testing.T) {
	n := parseOK(t, `{
		"num": 1499.90,
		"inteiro": 0,
		"grande": 12345678901234.99,
		"texto": "VENDA",
		"nulo": null,
		"bool": true,
		"identificador": "000998877",
		"data": "2026-08-03",
		"data_br": "03/08/2026",
		"data_hora": "2026-09-02T10:00:00-03:00"
	}`)

	quero := map[string]struct {
		valor string
		kind  sheet.Kind
	}{
		"num":     {"1499.90", sheet.KindNumber},
		"inteiro": {"0", sheet.KindNumber},
		// O literal do JSON passa inteiro: por float64 viraria 1.2345678901234e+13.
		"grande":        {"12345678901234.99", sheet.KindNumber},
		"texto":         {"VENDA", sheet.KindText},
		"nulo":          {"", sheet.KindText},
		"bool":          {"true", sheet.KindText},
		"identificador": {"000998877", sheet.KindText}, // texto: senão perde o zero à esquerda
		"data":          {"2026-08-03", sheet.KindDate},
		"data_br":       {"2026-08-03", sheet.KindDate},
		"data_hora":     {"2026-09-02T10:00:00-03:00", sheet.KindDateTime},
	}
	for _, f := range flatten(n) {
		w, ok := quero[f.Path]
		if !ok {
			t.Errorf("campo inesperado %q", f.Path)
			continue
		}
		if f.Value != w.valor || f.Kind != w.kind {
			t.Errorf("%s = %q/%v, quero %q/%v", f.Path, f.Value, f.Kind, w.valor, w.kind)
		}
		delete(quero, f.Path)
	}
	for p := range quero {
		t.Errorf("campo %q não foi achatado", p)
	}
}

func TestFlattenArrays(t *testing.T) {
	n := parseOK(t, `{
		"tags": ["loja-1", "promo"],
		"vazio": [],
		"objeto_vazio": {},
		"split": [{"conta":"9988","valor":100.00},{"conta":"7766","valor":50.00}]
	}`)

	got := map[string]string{}
	for _, f := range flatten(n) {
		got[f.Path] = f.Value
	}
	casos := map[string]string{
		// Array de escalares vira um campo só: uma coluna por tag deixaria a
		// planilha ilegível.
		"tags": "loja-1 | promo",
		// "vazio" e "objeto_vazio" não aparecem: uma coluna vazia em todas as
		// linhas não carrega informação nenhuma.
		// Array de objetos ganha índice em base 1, que é como a planilha lê.
		"split[1].conta": "9988",
		"split[1].valor": "100.00",
		"split[2].conta": "7766",
		"split[2].valor": "50.00",
	}
	for k, w := range casos {
		if got[k] != w {
			t.Errorf("%s = %q, quero %q", k, got[k], w)
		}
	}
	if len(got) != len(casos) {
		t.Errorf("campos = %v", got)
	}
}

func TestRecordsEncontraALista(t *testing.T) {
	casos := []struct {
		nome  string
		doc   string
		n     int
		chave string
	}{
		{"chave conhecida", `{"detalhes":[{"a":1},{"a":2}],"pagination":{}}`, 2, "detalhes"},
		{"array na raiz", `[{"a":1}]`, 1, ""},
		// Chave desconhecida: vale o primeiro array de objetos.
		{"chave desconhecida", `{"estabelecimento":"1","movimentacoes":[{"a":1}]}`, 1, "movimentacoes"},
		{"sem lista", `{"erro":"nada aqui"}`, 0, ""},
		// Um array de escalares não é a lista de registros.
		{"array de escalares", `{"avisos":["x"],"detalhes":[{"a":1}]}`, 1, "detalhes"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			regs, chave := records(parseOK(t, c.doc))
			if len(regs) != c.n || chave != c.chave {
				t.Errorf("records = %d registros em %q, quero %d em %q", len(regs), chave, c.n, c.chave)
			}
		})
	}
}

func TestPagination(t *testing.T) {
	casos := []struct {
		doc   string
		total int
		known bool
	}{
		{`{"pagination":{"totalPages":7}}`, 7, true},
		{`{"totalPages":3}`, 3, true},
		// Número em string: as APIs do PagBank misturam as duas formas.
		{`{"pagination":{"totalPages":"2"}}`, 2, true},
		{`{"detalhes":[]}`, 0, false},
		{`[{"a":1}]`, 0, false},
	}
	for _, c := range casos {
		got := pagination(parseOK(t, c.doc))
		if got.TotalPages != c.total || got.Known != c.known {
			t.Errorf("pagination(%s) = %+v, quero %d/%v", c.doc, got, c.total, c.known)
		}
	}
}

func TestParseRejeitaJSONInvalido(t *testing.T) {
	if _, err := parse([]byte(`{"a":`)); err == nil {
		t.Error("esperava erro")
	}
	if _, err := parse([]byte(`<html>erro</html>`)); err == nil {
		t.Error("esperava erro para resposta que não é JSON")
	}
}

func TestParseChaveRepetidaNaoDuplicaColuna(t *testing.T) {
	n := parseOK(t, `{"a":1,"a":2}`)
	got := flatten(n)
	if len(got) != 1 || got[0].Value != "2" {
		t.Errorf("flatten = %+v, quero um campo com o último valor", got)
	}
}
