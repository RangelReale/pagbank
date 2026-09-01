package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RangelReale/pagbank/internal/config"
	"github.com/RangelReale/pagbank/internal/web"
)

// Este teste roda o programa inteiro — servidor, página embutida, cliente da API
// legada, gravação do CSV — contra um PagSeguro de mentira. Nenhum byte sai da
// máquina e o navegador de quem roda os testes não é aberto: run recebe as duas
// dependências por opcoes justamente para isso.

// pagsegDeMentira serve as fixtures do pacote legacy.
func pagsegDeMentira(t *testing.T) *httptest.Server {
	t.Helper()
	fixture := func(nome string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "source", "legacy", "testdata", nome))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=ISO-8859-1")
		// O detalhe é /v2/transactions/{código}; a busca por data é /v2/transactions.
		if strings.Count(strings.Trim(r.URL.Path, "/"), "/") >= 2 {
			w.Write(fixture("detalhe-credito.xml"))
			return
		}
		w.Write(fixture("resumo.xml"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProgramaGeraOCSVPontaAPonta(t *testing.T) {
	api := pagsegDeMentira(t)

	dir := t.TempDir()
	t.Setenv(config.EnvEmail, "")
	t.Setenv(config.EnvToken, "")
	t.Setenv(config.EnvLegacyBaseURL, api.URL)

	enderecos := make(chan string, 1)
	parou := make(chan error, 1)
	go func() {
		parou <- run(opcoes{
			dir:    dir,
			abrir:  func(string) error { return nil },
			pronto: func(u string) { enderecos <- u },
		})
	}()

	var endereco string
	select {
	case endereco = <-enderecos:
	case err := <-parou:
		t.Fatalf("o programa parou antes de subir: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("o programa não subiu")
	}

	base, err := url.Parse(endereco)
	if err != nil {
		t.Fatal(err)
	}
	token := base.Query().Get("t")
	raiz := "http://" + base.Host

	// 1. Sem config.env preenchido, a página pede a credencial — e o modelo em
	//    branco já nasceu ao lado do executável.
	if _, err := os.Stat(web.CaminhoConfig(dir)); err != nil {
		t.Fatalf("o config.env modelo não foi criado: %v", err)
	}
	if html := baixar(t, raiz+"/?t="+token); !strings.Contains(html, "Falta configurar as credenciais") {
		t.Error("a página não pediu a credencial")
	}

	// 2. O usuário preenche o arquivo com o programa já rodando e recarrega.
	conteudo := config.EnvEmail + "=conta@empresa.com.br\n" + config.EnvToken + "=F1E2D3C4-token\n"
	if err := os.WriteFile(web.CaminhoConfig(dir), []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
	if html := baixar(t, raiz+"/?t="+token); !strings.Contains(html, "Gerar planilha") {
		t.Error("a página não trocou para o formulário depois do config.env preenchido")
	}

	// 3. A extração de verdade, contra o PagSeguro de mentira.
	q := url.Values{"t": {token}, "de": {"2026-08-01"}, "ate": {"2026-08-31"}, "detalhes": {"1"}}
	resp, err := http.Get(raiz + "/extrair?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var viuFim bool
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		linha := sc.Text()
		if strings.Contains(linha, "F1E2D3C4-token") {
			t.Fatalf("o token vazou no fluxo: %s", linha)
		}
		if linha == "event: erro" {
			sc.Scan()
			t.Fatalf("a extração falhou: %s", sc.Text())
		}
		if linha == "event: fim" {
			viuFim = true
			break
		}
	}
	if !viuFim {
		t.Fatal("o fluxo terminou sem o evento fim")
	}

	// 4. O CSV está no disco, com data e hora no nome.
	csvs := listarCSV(t, dir)
	if len(csvs) != 1 {
		t.Fatalf("%d CSV(s) em %s, quero 1: %v", len(csvs), dir, csvs)
	}
	if !strings.HasPrefix(csvs[0], "transacoes-") {
		t.Errorf("nome = %q, quero começar com transacoes-", csvs[0])
	}
	b, err := os.ReadFile(filepath.Join(dir, csvs[0]))
	if err != nil {
		t.Fatal(err)
	}
	// O contrato com o Excel em português: BOM UTF-8 e ponto e vírgula.
	if !strings.HasPrefix(string(b), "\uFEFF") {
		t.Error("o CSV saiu sem o BOM UTF-8")
	}
	if !strings.Contains(string(b), ";") {
		t.Error("o CSV saiu sem o separador ;")
	}

	// 5. O botão Encerrar desliga o programa, com o fluxo SSE ainda aberto —
	//    é o caso em que um Shutdown ingênuo ficaria pendurado para sempre.
	if _, err := http.Post(raiz+"/sair?t="+token, "", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-parou:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("o programa não encerrou: o Shutdown ficou pendurado no fluxo SSE")
	}
}

func baixar(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func listarCSV(t *testing.T, dir string) []string {
	t.Helper()
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var nomes []string
	for _, e := range entradas {
		if strings.HasSuffix(e.Name(), ".csv") {
			nomes = append(nomes, e.Name())
		}
	}
	return nomes
}
