package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RangelReale/pagbank/internal/config"
)

// semEnv limpa as variáveis de credencial para que o teste não dependa do
// ambiente de quem o roda.
func semEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		config.EnvEDIUser, config.EnvEDIToken, config.EnvEDIBaseURL,
		config.EnvEmail, config.EnvToken, config.EnvLegacyBaseURL,
	} {
		t.Setenv(k, "")
	}
}

// envInexistente devolve um caminho de .env que garantidamente não existe, para
// que o comando dependa só das variáveis de ambiente.
func envInexistente(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sem-env")
}

func TestEDIGravaOsCSVs(t *testing.T) {
	semEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/balances/") {
			w.Write([]byte(`[{"estabelecimento":"123456789","saldo_disponivel":10250.75}]`))
			return
		}
		w.Write([]byte(`{"detalhes":[
			{"movimento_api_codigo":"0001","valor_total_transacao":1499.90,"data_inicial_transacao":"2026-08-03"}
		],"pagination":{"page":1,"totalPages":1}}`))
	}))
	defer srv.Close()

	t.Setenv(config.EnvEDIUser, "123456789")
	t.Setenv(config.EnvEDIToken, "TOKEN-DE-TESTE-EDI")
	t.Setenv(config.EnvEDIBaseURL, srv.URL)

	out := t.TempDir()
	err := run(context.Background(), []string{"edi",
		"--from", "2026-08-03", "--to", "2026-08-04",
		"--types", "transactional,balances",
		"--out", out, "--env", envInexistente(t)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, nome := range []string{"transactional.csv", "balances.csv"} {
		b, err := os.ReadFile(filepath.Join(out, nome))
		if err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		conteudo := string(b)
		if !strings.HasPrefix(conteudo, "\uFEFF") {
			t.Errorf("%s: sem BOM, o Excel vai comer os acentos", nome)
		}
		if !strings.Contains(conteudo, "Data do movimento;") {
			t.Errorf("%s: sem a coluna da data consultada:\n%s", nome, conteudo)
		}
		// Dois dias, um registro por dia.
		if got := strings.Count(conteudo, "\r\n"); got != 3 {
			t.Errorf("%s: %d linhas, quero cabeçalho + 2:\n%s", nome, got, conteudo)
		}
	}
	if got := readFile(t, filepath.Join(out, "transactional.csv")); !strings.Contains(got, "1499,90") {
		t.Errorf("valor não saiu com vírgula decimal:\n%s", got)
	}
}

func TestTransacoesGravaOCSV(t *testing.T) {
	semEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=ISO-8859-1")
		w.Write([]byte(`<?xml version="1.0" encoding="ISO-8859-1"?>
<transactionSearchResult>
  <currentPage>1</currentPage>
  <resultsInThisPage>1</resultsInThisPage>
  <totalPages>1</totalPages>
  <transactions>
    <transaction>
      <date>2026-08-03T15:46:12.000-03:00</date>
      <code>9E884542</code>
      <reference>PEDIDO-1</reference>
      <type>1</type>
      <status>3</status>
      <paymentMethod><type>1</type></paymentMethod>
      <grossAmount>1499.90</grossAmount>
      <netAmount>1440.04</netAmount>
    </transaction>
  </transactions>
</transactionSearchResult>`))
	}))
	defer srv.Close()

	t.Setenv(config.EnvEmail, "conta@empresa.com.br")
	t.Setenv(config.EnvToken, "TOKEN-DE-TESTE-PAINEL")
	t.Setenv(config.EnvLegacyBaseURL, srv.URL)

	out := t.TempDir()
	// O período usa datas recentes para não esbarrar no limite de 6 meses.
	err := run(context.Background(), []string{"transacoes",
		"--from", hojeMenos(t, 3), "--out", out, "--env", envInexistente(t)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	got := readFile(t, filepath.Join(out, "transacoes.csv"))
	for _, want := range []string{"03/08/2026 15:46:12", "Paga", "Cartão de crédito", "1499,90"} {
		if !strings.Contains(got, want) {
			t.Errorf("CSV não contém %q:\n%s", want, got)
		}
	}
}

func TestErroDeCredencialEAcionavel(t *testing.T) {
	semEnv(t)

	err := run(context.Background(), []string{"edi",
		"--from", "2026-08-01", "--to", "2026-08-02", "--env", envInexistente(t)})
	if err == nil {
		t.Fatal("esperava erro")
	}
	for _, want := range []string{config.EnvEDIUser, config.EnvEDIToken, "README.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("erro não menciona %q: %v", want, err)
		}
	}
}

func TestErrosDeUso(t *testing.T) {
	semEnv(t)
	env := envInexistente(t)

	casos := []struct {
		nome  string
		args  []string
		quero string
	}{
		{"sem comando", nil, "informe um comando"},
		{"comando inexistente", []string{"extrato"}, `"extrato" não existe`},
		{"sem --from", []string{"edi", "--env", env}, "--from"},
		{"data inválida", []string{"edi", "--from", "01/08/2026", "--env", env}, "AAAA-MM-DD"},
		{"intervalo invertido", []string{"edi", "--from", "2026-08-10", "--to", "2026-08-01", "--env", env}, "anterior a --from"},
		{"tipo inexistente", []string{"edi", "--from", "2026-08-01", "--types", "vendas", "--env", env}, "transactional"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			err := run(context.Background(), c.args)
			if err == nil {
				t.Fatalf("esperava erro")
			}
			if !strings.Contains(err.Error(), c.quero) {
				t.Errorf("erro = %v, quero conter %q", err, c.quero)
			}
		})
	}
}

func TestVersionEHelpNaoFalham(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"help"}} {
		if err := run(context.Background(), args); err != nil {
			t.Errorf("run(%v): %v", args, err)
		}
	}
}

func TestMilhar(t *testing.T) {
	casos := map[int]string{0: "0", 7: "7", 999: "999", 1000: "1.000", 1482: "1.482", 1234567: "1.234.567", -1500: "-1.500"}
	for n, want := range casos {
		if got := milhar(n); got != want {
			t.Errorf("milhar(%d) = %q, quero %q", n, got, want)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" financial , , balances ")
	if len(got) != 2 || got[0] != "financial" || got[1] != "balances" {
		t.Errorf("splitList = %q", got)
	}
	if got := splitList(""); got != nil {
		t.Errorf("splitList(\"\") = %q, quero nil", got)
	}
}

// hojeMenos devolve uma data recente: a API legada só guarda 6 meses, e o
// cliente valida isso contra o relógio de verdade.
func hojeMenos(t *testing.T, dias int) string {
	t.Helper()
	return time.Now().AddDate(0, 0, -dias).Format("2006-01-02")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
