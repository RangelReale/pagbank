package legacy

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/RangelReale/pagbank/internal/sheet"
)

// comDetalhe sobe um servidor que responde a busca por data com o resumo — sem
// os campos que a API de verdade omite — e delega /v2/transactions/{código} ao
// handler recebido. Devolve também os caminhos pedidos, na ordem.
func comDetalhe(t *testing.T, detalhe http.HandlerFunc) (*Client, *[]string, *[]url.Values) {
	t.Helper()

	var mu sync.Mutex
	var caminhos []string
	c, pedidos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		caminhos = append(caminhos, r.URL.Path)
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/v2/transactions/") {
			detalhe(w, r)
			return
		}
		w.Write(fixture(t, "resumo.xml"))
	})
	c.SemDetalhes = false
	return c, &caminhos, pedidos
}

// porCodigo responde o detalhe das duas transações do resumo.
func porCodigo(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch path.Base(r.URL.Path) {
		case "E56B1AB8-91B0-427B-9059-EEEE1BD69BD9":
			w.Write(fixture(t, "detalhe-credito.xml"))
		case "3871BE90-BB3B-5CD7-8D15-CBB20FA8A636":
			w.Write(fixture(t, "detalhe-pix.xml"))
		default:
			http.Error(w, "Not Found", http.StatusNotFound)
		}
	}
}

func TestFetchDetalhesPreencheOQueOResumoNaoTraz(t *testing.T) {
	c, caminhos, pedidos := comDetalhe(t, porCodigo(t))

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-30"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tab := res.Tables[0]
	if len(tab.Rows) != 2 {
		t.Fatalf("linhas = %d, quero 2", len(tab.Rows))
	}

	// As quatro primeiras são exatamente as colunas que saíram em branco na
	// primeira extração real: a busca por data não traz nenhuma delas.
	casos := []struct{ coluna, want string }{
		{"Parcelas", "4"},
		{"Itens", "1"},
		{"Última atualização", "2026-08-31T16:04:12-03:00"},
		{"Meio de pagamento (detalhe)", "101"},
		{"Referência", "MAQUININHA-77"},
		// O que o resumo já trazia continua igual.
		{"Valor bruto", "48.00"},
		{"Código", "E56B1AB8-91B0-427B-9059-EEEE1BD69BD9"},
	}
	for _, cs := range casos {
		if got := tab.Rows[0][colIndex(t, tab, cs.coluna)]; got != cs.want {
			t.Errorf("%s = %q, quero %q", cs.coluna, got, cs.want)
		}
	}
	if got := tab.Rows[1][colIndex(t, tab, "Parcelas")]; got != "1" {
		t.Errorf("parcelas do PIX = %q, quero 1", got)
	}

	// Uma busca e um detalhe por transação; o detalhe leva as credenciais.
	if len(*caminhos) != 3 {
		t.Fatalf("requisições = %v, quero busca + 2 detalhes", *caminhos)
	}
	if want := "/v2/transactions/E56B1AB8-91B0-427B-9059-EEEE1BD69BD9"; (*caminhos)[1] != want {
		t.Errorf("caminho do detalhe = %q, quero %q", (*caminhos)[1], want)
	}
	if q := (*pedidos)[1]; q.Get("email") != testEmail || q.Get("token") != testToken {
		t.Errorf("detalhe sem credenciais: %v", q)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("avisos = %v, quero nenhum: o detalhe veio para as duas", res.Warnings)
	}
}

func TestFetchDetalhesEnviaAcceptComCharset(t *testing.T) {
	var mu sync.Mutex
	var accept string
	c, _, _ := comDetalhe(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		accept = r.Header.Get("Accept")
		mu.Unlock()
		porCodigo(t)(w, r)
	})

	if _, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-30")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// O 406 do RESTEasy vale para os dois endpoints, não só para a busca.
	if want := "application/xml;charset=ISO-8859-1"; accept != want {
		t.Errorf("Accept = %q, quero %q", accept, want)
	}
}

func TestFetchSemDetalhesAvisaEDeixaAsColunasEmBranco(t *testing.T) {
	c, caminhos, _ := comDetalhe(t, porCodigo(t))
	c.SemDetalhes = true

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-30"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tab := res.Tables[0]
	for _, coluna := range []string{"Parcelas", "Itens", "Última atualização", "Meio de pagamento (detalhe)"} {
		if got := tab.Rows[0][colIndex(t, tab, coluna)]; got != "" {
			t.Errorf("%s = %q, quero vazia com --sem-detalhes", coluna, got)
		}
	}
	if len(*caminhos) != 1 {
		t.Errorf("requisições = %v, quero só a busca por data", *caminhos)
	}
	// Sem o aviso, as colunas vazias não teriam explicação nenhuma — foi
	// exatamente o que aconteceu na primeira extração real.
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "em branco") {
		t.Errorf("avisos = %v, quero um explicando as colunas vazias", res.Warnings)
	}
}

func TestFetchSemTransacoesNaoAvisaSobreDetalhes(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "vazia.xml"))
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-10"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("avisos = %v, quero nenhum quando não veio transação", res.Warnings)
	}
}

func TestFetchDetalhesToleraFalhaEmUmaTransacao(t *testing.T) {
	c, _, _ := comDetalhe(t, func(w http.ResponseWriter, r *http.Request) {
		if path.Base(r.URL.Path) == "3871BE90-BB3B-5CD7-8D15-CBB20FA8A636" {
			w.Write(fixture(t, "detalhe-pix.xml"))
			return
		}
		http.Error(w, "Not Found", http.StatusNotFound)
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-30"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tab := res.Tables[0]
	if len(tab.Rows) != 2 {
		t.Fatalf("linhas = %d, quero 2: uma falha de detalhe não descarta a transação", len(tab.Rows))
	}
	// A linha sem detalhe sobrevive com o que o resumo trouxe.
	if got := tab.Rows[0][colIndex(t, tab, "Valor bruto")]; got != "48.00" {
		t.Errorf("valor bruto = %q, quero o do resumo", got)
	}
	if got := tab.Rows[0][colIndex(t, tab, "Parcelas")]; got != "" {
		t.Errorf("parcelas = %q, quero vazia: o detalhe falhou", got)
	}
	if got := tab.Rows[1][colIndex(t, tab, "Parcelas")]; got != "1" {
		t.Errorf("parcelas da outra = %q, quero 1", got)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "sem detalhe") {
		t.Errorf("avisos = %v, quero um sobre o detalhe que falhou", res.Warnings)
	}
}

func TestMergeNaoSobrescreveOQueOResumoTrouxe(t *testing.T) {
	resumo := transaction{Code: "DO-RESUMO", GrossAmount: "48.00"}
	detalhe := transaction{Code: "DO-DETALHE", GrossAmount: "99.00", InstallmentCount: "4"}
	detalhe.PaymentMethod.Code = "101"

	got := merge(resumo, detalhe)
	if got.Code != "DO-RESUMO" || got.GrossAmount != "48.00" {
		t.Errorf("o detalhe sobrescreveu o resumo: %+v", got)
	}
	if got.InstallmentCount != "4" || got.PaymentMethod.Code != "101" {
		t.Errorf("o campo vazio não foi preenchido: %+v", got)
	}
}

func TestGoldenCSVComDetalhes(t *testing.T) {
	c, _, _ := comDetalhe(t, porCodigo(t))

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-30"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var buf bytes.Buffer
	if err := sheet.Write(&buf, res.Tables[0], sheet.DefaultOptions()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	golden := filepath.Join("testdata", "transacoes-detalhes.csv")
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (rode: go test ./internal/source/legacy -update)", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("CSV divergiu do golden:\nobtido:\n%s", buf.String())
	}
}
