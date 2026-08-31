package edi

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	testUser  = "123456789"
	testToken = "EDI-TOKEN-DE-TESTE-9988"
)

func fixture(t *testing.T, nome string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", nome))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// servidor sobe um EDI de mentira e devolve o Client apontado para ele, junto
// com os caminhos requisitados.
func servidor(t *testing.T, h http.HandlerFunc) (*Client, *[]string) {
	t.Helper()

	var mu sync.Mutex
	var caminhos []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		caminhos = append(caminhos, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := config.EDI{User: testUser, Token: testToken, BaseURL: srv.URL}
	hc := httpx.New(httpx.NewRedactor(cfg.Secrets()...))
	hc.MinInterval = 0
	hc.BaseDelay = time.Millisecond

	return New(cfg, hc), &caminhos
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

// tipoDoCaminho extrai o tipo de movimento de /movement/v3.00/{tipo}/{data}.
func tipoDoCaminho(path string) string {
	partes := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(partes) < 3 {
		return ""
	}
	return partes[2]
}

func responder(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch tipoDoCaminho(r.URL.Path) {
		case "transactional":
			if r.URL.Query().Get("pageNumber") == "1" {
				w.Write(fixture(t, "transactional-p1.json"))
				return
			}
			w.Write(fixture(t, "transactional-p2.json"))
		case "balances":
			w.Write(fixture(t, "balances.json"))
		case "financial":
			w.Write(fixture(t, "financial.json"))
		default:
			w.Write(fixture(t, "vazio.json"))
		}
	}
}

func TestFetchMontaAURLEOBasicAuth(t *testing.T) {
	var auth string
	c, caminhos := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write(fixture(t, "vazio.json"))
	})
	c.Types = []string{"transactional"}

	if _, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-03")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := "/movement/v3.00/transactional/2026-08-03?pageNumber=1&pageSize=1000"
	if len(*caminhos) != 1 || (*caminhos)[0] != want {
		t.Errorf("caminhos = %v, quero [%s]", *caminhos, want)
	}
	esperado := "Basic " + base64.StdEncoding.EncodeToString([]byte(testUser+":"+testToken))
	if auth != esperado {
		t.Errorf("Authorization = %q, quero o Basic de estabelecimento:token", auth)
	}
}

func TestFetchPercorreDiasETipos(t *testing.T) {
	c, caminhos := servidor(t, responder(t))
	c.Types = []string{"transactional", "balances"}

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-05"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// 3 dias x (2 páginas de transactional + 1 de balances) = 9 requisições.
	if len(*caminhos) != 9 {
		t.Errorf("requisições = %d, quero 9: %v", len(*caminhos), *caminhos)
	}
	if len(res.Tables) != 2 {
		t.Fatalf("tabelas = %d, quero uma por tipo", len(res.Tables))
	}
	if res.Tables[0].Name != "transactional" || res.Tables[1].Name != "balances" {
		t.Errorf("nomes = %q e %q", res.Tables[0].Name, res.Tables[1].Name)
	}
	if got := len(res.Tables[0].Rows); got != 9 { // 3 registros/dia x 3 dias
		t.Errorf("linhas de transactional = %d, quero 9", got)
	}
	if got := len(res.Tables[1].Rows); got != 3 {
		t.Errorf("linhas de balances = %d, quero 3", got)
	}
}

func TestFetchColunaQueSoApareceNaSegundaPagina(t *testing.T) {
	c, _ := servidor(t, responder(t))
	c.Types = []string{"transactional"}

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-03"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tab := res.Tables[0]
	i := colIndex(t, tab, "data_prevista_pagamento")
	if got := tab.Rows[0][i]; got != "" {
		t.Errorf("linha da página 1 = %q, quero célula vazia", got)
	}
	if got, want := tab.Rows[2][i], "2026-09-02T10:00:00-03:00"; got != want {
		t.Errorf("linha da página 2 = %q, quero %q", got, want)
	}
}

func TestFetchAcrescentaADataConsultada(t *testing.T) {
	c, _ := servidor(t, responder(t))
	c.Types = []string{"balances"}

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-04"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tab := res.Tables[0]
	if tab.Columns[0].Header != "Data do movimento" || tab.Columns[0].Kind != sheet.KindDate {
		t.Fatalf("primeira coluna = %+v", tab.Columns[0])
	}
	if tab.Rows[0][0] != "2026-08-03" || tab.Rows[1][0] != "2026-08-04" {
		t.Errorf("datas = %q e %q", tab.Rows[0][0], tab.Rows[1][0])
	}
}

func TestFetchListaSobChaveDesconhecida(t *testing.T) {
	c, _ := servidor(t, responder(t))
	c.Types = []string{"financial"}

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-03"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := len(res.Tables[0].Rows); got != 1 {
		t.Fatalf("linhas = %d, quero 1 (achou a lista sob 'movimentacoes')", got)
	}
	if got := res.Tables[0].Rows[0][colIndex(t, res.Tables[0], "parcela")]; got != "1/3" {
		t.Errorf("parcela = %q", got)
	}
}

func TestFetchParaSemTotalDePaginas(t *testing.T) {
	// balances.json é um array na raiz, sem paginação: a única pista de fim é a
	// página ter vindo com menos registros do que o pageSize.
	c, caminhos := servidor(t, responder(t))
	c.Types = []string{"balances"}

	if _, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-03")); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(*caminhos) != 1 {
		t.Errorf("requisições = %d, quero 1", len(*caminhos))
	}
}

func TestFetchAvisaVALIDADOFalse(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "2026-08-04") {
			w.Header().Set("VALIDADO", "FALSE")
		}
		w.Write(fixture(t, "vazio.json"))
	})
	c.Types = []string{"transactional"}

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-04"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("avisos = %v, quero um", res.Warnings)
	}
	if !strings.Contains(res.Warnings[0], "transactional/2026-08-04") {
		t.Errorf("aviso não nomeia o dia: %s", res.Warnings[0])
	}
	if strings.Contains(res.Warnings[0], "2026-08-03") {
		t.Errorf("aviso cita dia que estava validado: %s", res.Warnings[0])
	}
}

func TestFetchTrata404ComoDiaSemMovimento(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "2026-08-04") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Write(fixture(t, "transactional-p1.json"))
	})
	c.Types = []string{"transactional"}

	// Um 404 no meio do período não pode abortar a extração inteira.
	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-05"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "2026-08-04") {
		t.Fatalf("avisos = %v", res.Warnings)
	}
	if got := len(res.Tables[0].Rows); got == 0 {
		t.Errorf("os outros dias deveriam ter sido extraídos")
	}
}

func TestFetchExplicaCredenciaisRecusadas(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	c.Types = []string{"transactional"}

	_, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-03"))
	if err == nil {
		t.Fatal("esperava erro")
	}
	for _, want := range []string{config.EnvEDIUser, config.EnvEDIToken, "chamado"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("erro não menciona %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("o token vazou: %v", err)
	}
}

func TestFetchErroDeJSON(t *testing.T) {
	c, _ := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>manutenção</html>")
	})
	c.Types = []string{"transactional"}

	_, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-03"))
	if err == nil || !strings.Contains(err.Error(), "JSON inesperada") {
		t.Fatalf("erro = %v", err)
	}
}

func TestValidateTypes(t *testing.T) {
	if err := ValidateTypes([]string{"transactional", "balances"}); err != nil {
		t.Errorf("ValidateTypes: %v", err)
	}
	if err := ValidateTypes([]string{"vendas"}); err == nil || !strings.Contains(err.Error(), "transactional") {
		t.Errorf("erro = %v, quero a lista de tipos válidos", err)
	}
	if err := ValidateTypes(nil); err == nil {
		t.Error("lista vazia deveria ser erro")
	}
}

func TestPageSizeLimitado(t *testing.T) {
	c := &Client{PageSize: 5000}
	if got := c.pageSize(); got != DefaultPageSize {
		t.Errorf("pageSize = %d, quero o teto %d", got, DefaultPageSize)
	}
	c.PageSize = 0
	if got := c.pageSize(); got != DefaultPageSize {
		t.Errorf("pageSize = %d", got)
	}
	c.PageSize = 100
	if got := c.pageSize(); got != 100 {
		t.Errorf("pageSize = %d", got)
	}
}

func TestGoldenCSV(t *testing.T) {
	c, _ := servidor(t, responder(t))
	c.Types = []string{"transactional", "balances"}

	res, err := c.Fetch(context.Background(), periodo(t, "2026-08-03", "2026-08-03"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for _, tab := range res.Tables {
		var buf bytes.Buffer
		if err := sheet.Write(&buf, tab, sheet.DefaultOptions()); err != nil {
			t.Fatalf("Write: %v", err)
		}
		golden := filepath.Join("testdata", sheet.FileName(tab.Name))
		if *update {
			if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (rode: go test ./internal/source/edi -update)", err)
		}
		if !bytes.Equal(buf.Bytes(), want) {
			t.Errorf("%s divergiu do golden:\nobtido:\n%s", golden, buf.String())
		}
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
