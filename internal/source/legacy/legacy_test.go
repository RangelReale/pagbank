package legacy

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RangelReale/pagbank/internal/config"
	"github.com/RangelReale/pagbank/internal/httpx"
	"github.com/RangelReale/pagbank/internal/sheet"
	"github.com/RangelReale/pagbank/internal/source"
)

var update = flag.Bool("update", false, "regrava os arquivos golden em testdata")

const (
	testEmail = "conta@empresa.com.br"
	testToken = "F1E2D3C4-token-de-teste"
)

func fixture(t *testing.T, nome string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", nome))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// servidor sobe um PagSeguro de mentira e devolve o Client apontado para ele,
// junto com a lista de requisições recebidas.
func servidor(t *testing.T, h http.HandlerFunc) (*Client, *[]url.Values) {
	t.Helper()

	var mu sync.Mutex
	var pedidos []url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pedidos = append(pedidos, r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml; charset=ISO-8859-1")
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Legacy{Email: testEmail, Token: testToken, BaseURL: srv.URL}
	hc := httpx.New(httpx.NewRedactor(cfg.Secrets()...))
	hc.MinInterval = 0
	hc.BaseDelay = time.Millisecond

	c := New(cfg, hc)
	// "Hoje" fixo: o limite de seis meses não pode depender da data real.
	c.Now = func() time.Time { return time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC) }
	return c, &pedidos
}

func periodo(t *testing.T, de, ate string) source.Period {
	t.Helper()
	from, err := source.ParseDate(de)
	if err != nil {
		t.Fatal(err)
	}
	to, err := source.ParseDate(ate)
	if err != nil {
		t.Fatal(err)
	}
	p, err := source.NewPeriod(from, to)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFetchPaginaEDeduplica(t *testing.T) {
	c, pedidos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Write(fixture(t, "pagina1.xml"))
			return
		}
		w.Write(fixture(t, "pagina2.xml"))
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-30"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Tables) != 1 || res.Tables[0].Name != TableName {
		t.Fatalf("tabelas = %+v", res.Tables)
	}
	// Quatro registros vieram, mas um se repete entre as páginas.
	if got := res.Records(); got != 3 {
		t.Errorf("registros = %d, quero 3", got)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "repetida") {
		t.Errorf("avisos = %v, quero um sobre duplicata", res.Warnings)
	}
	if len(*pedidos) != 2 {
		t.Fatalf("requisições = %d, quero 2 (parou ao chegar na última página)", len(*pedidos))
	}
}

func TestFetchEnviaAsCredenciaisEOIntervalo(t *testing.T) {
	c, pedidos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "vazia.xml"))
	})

	if _, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-10")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	q := (*pedidos)[0]
	casos := map[string]string{
		"email":          testEmail,
		"token":          testToken,
		"initialDate":    "2026-08-01T00:00:00",
		"finalDate":      "2026-08-10T23:59:59", // fim do último dia, não meia-noite
		"page":           "1",
		"maxPageResults": "1000",
	}
	for k, want := range casos {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, quero %q", k, got, want)
		}
	}
}

func TestFetchEnviaAcceptComCharset(t *testing.T) {
	var mu sync.Mutex
	var accept string
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		accept = r.Header.Get("Accept")
		mu.Unlock()
		w.Write(fixture(t, "vazia.xml"))
	})

	if _, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-01")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// Sem o charset a API de produção responde 406: o RESTEasy dela compara o
	// media type com os parâmetros.
	if want := "application/xml;charset=ISO-8859-1"; accept != want {
		t.Errorf("Accept = %q, quero %q", accept, want)
	}
}

func TestFetchFatiaEmJanelasDeTrintaDias(t *testing.T) {
	c, pedidos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "vazia.xml"))
	})

	// 70 dias => 30 + 30 + 10.
	if _, err := c.Fetch(context.Background(), periodo(t, "2026-06-01", "2026-08-09")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(*pedidos) != 3 {
		t.Fatalf("requisições = %d, quero 3 janelas", len(*pedidos))
	}
	quero := [][2]string{
		{"2026-06-01T00:00:00", "2026-06-30T23:59:59"},
		{"2026-07-01T00:00:00", "2026-07-30T23:59:59"},
		{"2026-07-31T00:00:00", "2026-08-09T23:59:59"},
	}
	for i, w := range quero {
		q := (*pedidos)[i]
		if q.Get("initialDate") != w[0] || q.Get("finalDate") != w[1] {
			t.Errorf("janela %d = %s..%s, quero %s..%s", i, q.Get("initialDate"), q.Get("finalDate"), w[0], w[1])
		}
	}
}

func TestFetchDecodificaISO88591(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "pagina1.xml"))
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-10"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tab := res.Tables[0]
	i := colIndex(t, tab, "Referência")
	// A fixture está gravada em latin-1; sem o CharsetReader isto viria como
	// bytes soltos ou o decode falharia.
	if got, want := tab.Rows[1][i], "Manutenção de serviço - código ÚNICO"; got != want {
		t.Errorf("referência = %q, quero %q", got, want)
	}
}

func TestFetchTraduzCodigosEPreservaOsCrus(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "pagina1.xml"))
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-10"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tab := res.Tables[0]
	casos := []struct{ coluna, want string }{
		{"Status", "Paga"},
		{"Status (código)", "3"},
		{"Meio de pagamento", "Cartão de crédito"},
		{"Meio de pagamento (código)", "1"},
		// tipo não tem tabela confiável: cai no código, nunca em célula vazia.
		{"Tipo", "código 1"},
		{"Tipo (código)", "1"},
	}
	for _, cs := range casos {
		if got := tab.Rows[0][colIndex(t, tab, cs.coluna)]; got != cs.want {
			t.Errorf("%s = %q, quero %q", cs.coluna, got, cs.want)
		}
	}
	// Código desconhecido também vira "código N".
	if got := tab.Rows[1][colIndex(t, tab, "Status")]; got != "Retenção temporária" {
		t.Errorf("status 9 = %q", got)
	}
}

func TestFetchNormalizaDatas(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "pagina1.xml"))
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-10"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tab := res.Tables[0]
	// Milissegundos somem; o fuso da resposta é preservado, não convertido.
	if got, want := tab.Rows[0][colIndex(t, tab, "Data")], "2026-08-03T15:46:12-03:00"; got != want {
		t.Errorf("Data = %q, quero %q", got, want)
	}
	if k := tab.Columns[colIndex(t, tab, "Data")].Kind; k != sheet.KindDateTime {
		t.Errorf("Kind = %v, quero datetime", k)
	}
}

func TestFetchRecusaPeriodoForaDoHistorico(t *testing.T) {
	c, pedidos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "vazia.xml"))
	})

	// "Hoje" no teste é 31/08/2026, então o limite é 28/02/2026.
	_, err := c.Fetch(context.Background(), periodo(t, "2025-01-01", "2025-02-01"))
	if err == nil || !strings.Contains(err.Error(), "últimos 6 meses") {
		t.Fatalf("erro = %v", err)
	}
	if len(*pedidos) != 0 {
		t.Errorf("não deveria ter feito requisição alguma")
	}
}

func TestFetchAvisaPeriodoParcialmenteForaDoHistorico(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "vazia.xml"))
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2025-12-01", "2026-08-01"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "limite de 6 meses") {
		t.Fatalf("avisos = %v", res.Warnings)
	}
}

func TestFetchCortaOFinalDateEmAgora(t *testing.T) {
	c, pedidos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "vazia.xml"))
	})

	// "Hoje" no teste é 31/08/2026 00:00 UTC, ou seja, 30/08 às 21:00 em
	// Brasília. O fim do dia 30 ainda é futuro para a API, que recusaria a
	// consulta (código 13009): o valor tem que ser cortado em agora, menos a
	// folga de relógio.
	if _, err := c.Fetch(context.Background(), periodo(t, "2026-08-30", "2026-08-30")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	q := (*pedidos)[0]
	if got, want := q.Get("finalDate"), "2026-08-30T20:59:00"; got != want {
		t.Errorf("finalDate = %q, quero %q", got, want)
	}
	if got, want := q.Get("initialDate"), "2026-08-30T00:00:00"; got != want {
		t.Errorf("initialDate = %q, quero %q", got, want)
	}
}

func TestFetchAvisaPeriodoQueTerminaNoFuturo(t *testing.T) {
	c, pedidos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "vazia.xml"))
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-28", "2026-09-10"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "no futuro") {
		t.Fatalf("avisos = %v", res.Warnings)
	}
	// Uma janela só, e nenhuma consulta além de hoje.
	if len(*pedidos) != 1 {
		t.Fatalf("requisições = %d, quero 1", len(*pedidos))
	}
	if got, want := (*pedidos)[0].Get("finalDate"), "2026-08-30T20:59:00"; got != want {
		t.Errorf("finalDate = %q, quero %q", got, want)
	}
}

func TestFetchRecusaPeriodoQueComecaNoFuturo(t *testing.T) {
	c, pedidos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "vazia.xml"))
	})

	_, err := c.Fetch(context.Background(), periodo(t, "2026-09-05", "2026-09-10"))
	if err == nil || !strings.Contains(err.Error(), "no futuro") {
		t.Fatalf("erro = %v", err)
	}
	if len(*pedidos) != 0 {
		t.Errorf("não deveria ter feito requisição alguma")
	}
}

func TestFetchExplicaErroDaAPI(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(fixture(t, "erro.xml"))
	})

	_, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-10"))
	if err == nil {
		t.Fatal("esperava erro")
	}
	if !strings.Contains(err.Error(), "13005") || !strings.Contains(err.Error(), "initialDate") {
		t.Errorf("erro = %v, quero a mensagem da API", err)
	}
}

func TestFetchExplicaCredenciaisRecusadas(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})

	_, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-10"))
	if err == nil {
		t.Fatal("esperava erro")
	}
	if !strings.Contains(err.Error(), config.EnvToken) {
		t.Errorf("erro = %v, quero a variável a conferir", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("o token vazou: %v", err)
	}
}

func TestFetchNaoVazaOTokenNoErro(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "erro interno processando %s", r.URL.String())
	})
	c.HTTP.MaxAttempts = 1

	_, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-10"))
	if err == nil {
		t.Fatal("esperava erro")
	}
	for _, segredo := range []string{testToken, testEmail} {
		if strings.Contains(err.Error(), segredo) {
			t.Errorf("segredo %q vazou em: %v", segredo, err)
		}
	}
}

func TestGoldenCSV(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Write(fixture(t, "pagina1.xml"))
			return
		}
		w.Write(fixture(t, "pagina2.xml"))
	})

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-01", "2026-08-30"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var buf bytes.Buffer
	if err := sheet.Write(&buf, res.Tables[0], sheet.DefaultOptions()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	golden := filepath.Join("testdata", "transacoes.csv")
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

func TestLatin1ReaderComBufferPequeno(t *testing.T) {
	// O Read tem que sobreviver a um destino menor que o rune codificado.
	origem := []byte{0x63, 0xE3, 0x6F} // "cão" em latin-1
	r := &latin1Reader{r: bytes.NewReader(origem)}

	var out bytes.Buffer
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if got := out.String(); got != "cão" {
		t.Errorf("leitura = %q, quero %q", got, "cão")
	}
}

func colIndex(t *testing.T, tab sheet.Table, header string) int {
	t.Helper()
	for i, c := range tab.Columns {
		if c.Header == header {
			return i
		}
	}
	t.Fatalf("coluna %q não existe em %v", header, tab.Columns)
	return -1
}
