package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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
	"github.com/RangelReale/pagbank/internal/sheet"
	"github.com/RangelReale/pagbank/internal/source"
)

const (
	testToken = "TOKEN-DE-TESTE-DA-SESSAO"
	testEmail = "conta@empresa.com.br"
	testSenha = "F1E2D3C4-token-do-pagbank"
)

// fonteFake é uma source.Source de mentira: nenhum teste deste pacote toca a
// rede. O canal liberar deixa o teste segurar o Fetch pelo tempo que precisar,
// que é como se testa uma extração longa sem um único sleep.
type fonteFake struct {
	logf      func(string, ...any)
	progresso func(feitos, total int)

	// durante roda dentro do Fetch, para o teste emitir o progresso que quiser.
	durante func(logf func(string, ...any), progresso func(feitos, total int))
	res     *source.Result
	err     error

	entrou  chan struct{} // fechado quando o Fetch começa
	liberar chan struct{} // o Fetch espera aqui, se não for nil
	cancelo chan struct{} // fechado quando o ctx cancela o Fetch
}

func (f *fonteFake) Name() string { return "fonte de mentira" }

func (f *fonteFake) Fetch(ctx context.Context, p source.Period) (*source.Result, error) {
	if f.entrou != nil {
		close(f.entrou)
	}
	if f.durante != nil {
		f.durante(f.logf, f.progresso)
	}
	if f.liberar != nil {
		select {
		case <-f.liberar:
		case <-ctx.Done():
			if f.cancelo != nil {
				close(f.cancelo)
			}
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.res != nil {
		return f.res, nil
	}
	return &source.Result{Tables: []sheet.Table{tabelaExemplo()}}, nil
}

// fabricaDe monta uma Fabrica que devolve sempre o mesmo fake, já ligado aos
// ganchos de progresso que o servidor entregou.
func fabricaDe(f *fonteFake) Fabrica {
	return func(_ config.Legacy, _ Pedido, logf func(string, ...any), progresso func(int, int)) source.Source {
		f.logf, f.progresso = logf, progresso
		return f
	}
}

func tabelaExemplo() sheet.Table {
	return sheet.Table{
		Name:    "transacoes",
		Columns: []sheet.Column{{Header: "Código"}, {Header: "Valor bruto", Kind: sheet.KindNumber}},
		Rows:    [][]string{{"1A2B", "1234.56"}, {"3C4D", "99.90"}},
	}
}

// semAmbiente limpa as credenciais do ambiente do processo, que têm prioridade
// sobre o arquivo e fariam o teste depender de quem o roda.
func semAmbiente(t *testing.T) {
	t.Helper()
	for _, k := range []string{config.EnvEmail, config.EnvToken, config.EnvLegacyBaseURL} {
		t.Setenv(k, "")
	}
}

// comConfig grava um config.env preenchido em dir.
func comConfig(t *testing.T, dir string) {
	t.Helper()
	conteudo := config.EnvEmail + "=" + testEmail + "\n" + config.EnvToken + "=" + testSenha + "\n"
	if err := os.WriteFile(CaminhoConfig(dir), []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
}

// opcoesDeTeste são as opções comuns: pasta temporária, relógio fixo e as duas
// esperas do watchdog longas o bastante para não dispararem sozinhas — quem
// testa o watchdog as encurta.
func opcoesDeTeste(t *testing.T) Opcoes {
	t.Helper()
	semAmbiente(t)
	return Opcoes{
		Dir:                   t.TempDir(),
		Token:                 testToken,
		Versao:                "teste",
		Agora:                 func() time.Time { return time.Date(2026, 9, 1, 14, 30, 22, 0, time.UTC) },
		Fonte:                 fabricaDe(&fonteFake{}),
		EsperaPrimeiroCliente: time.Hour,
		EsperaSemCliente:      time.Hour,
		EsperaSemJanela:       10 * time.Millisecond,
	}
}

// servidor sobe a interface e devolve o endereço e a pasta de trabalho.
func servidor(t *testing.T, o Opcoes) (*httptest.Server, *Servidor) {
	t.Helper()
	s := New(o)
	t.Cleanup(s.Parar)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv, s
}

// evento é um evento SSE já separado em nome e carga.
type evento struct {
	nome  string
	dados []byte
}

// lerAte consome o fluxo SSE até o evento chamado ate, ou até o fim do corpo.
// Não há espera em lugar nenhum: a leitura bloqueia até o servidor escrever.
func lerAte(t *testing.T, body io.Reader, ate string) []evento {
	t.Helper()
	var eventos []evento
	var atual evento

	sc := bufio.NewScanner(body)
	for sc.Scan() {
		linha := sc.Text()
		switch {
		case strings.HasPrefix(linha, "event: "):
			atual.nome = strings.TrimPrefix(linha, "event: ")
		case strings.HasPrefix(linha, "data: "):
			atual.dados = []byte(strings.TrimPrefix(linha, "data: "))
		case linha == "" && atual.nome != "":
			eventos = append(eventos, atual)
			if atual.nome == ate {
				return eventos
			}
			atual = evento{}
		}
	}
	return eventos
}

// ultimo devolve o último evento com o nome dado, ou falha o teste.
func ultimo(t *testing.T, eventos []evento, nome string) evento {
	t.Helper()
	for i := len(eventos) - 1; i >= 0; i-- {
		if eventos[i].nome == nome {
			return eventos[i]
		}
	}
	t.Fatalf("nenhum evento %q em %d evento(s)", nome, len(eventos))
	return evento{}
}

// pedirExtracao abre o fluxo de extração com o período dado.
func pedirExtracao(t *testing.T, srv *httptest.Server, de, ate string) *http.Response {
	t.Helper()
	q := url.Values{"t": {testToken}, "de": {de}, "ate": {ate}, "detalhes": {"1"}}
	resp, err := http.Get(srv.URL + "/extrair?" + q.Encode())
	if err != nil {
		t.Fatalf("GET /extrair: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// avisador conta as chamadas de Encerrar sem correr risco de fechar o canal
// duas vezes: o watchdog e o botão podem disparar o encerramento juntos.
func encerrador() (func(), chan struct{}) {
	ch := make(chan struct{})
	var uma sync.Once
	return func() { uma.Do(func() { close(ch) }) }, ch
}

func TestPaginaPedeCredencialQuandoFaltaConfig(t *testing.T) {
	o := opcoesDeTeste(t)
	srv, _ := servidor(t, o)
	dir := o.Dir

	resp, err := http.Get(srv.URL + "/?t=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(resp.Body)
	html := string(corpo)

	for _, quero := range []string{"Falta configurar as credenciais", config.EnvEmail, config.EnvToken, CaminhoConfig(dir)} {
		if !strings.Contains(html, quero) {
			t.Errorf("a página não cita %q", quero)
		}
	}
	if strings.Contains(html, "Gerar planilha") {
		t.Error("a página mostrou o formulário sem haver credencial")
	}
}

func TestPaginaMostraOFormularioComCredencial(t *testing.T) {
	o := opcoesDeTeste(t)
	srv, _ := servidor(t, o)
	dir := o.Dir
	comConfig(t, dir)

	resp, err := http.Get(srv.URL + "/?t=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	corpo, _ := io.ReadAll(resp.Body)
	html := string(corpo)

	if !strings.Contains(html, "Gerar planilha") {
		t.Error("a página não mostrou o formulário")
	}
	// O limite de histórico da API vira o mínimo do seletor de datas, para o
	// navegador impedir o pedido que a API recusaria.
	if !strings.Contains(html, `min="2026-03-01"`) {
		t.Error("a página não limitou a data mínima em 6 meses")
	}
	if strings.Contains(html, testSenha) {
		t.Error("a página ecoou o token")
	}
}

func TestGarantirConfigModeloNaoSobrescreve(t *testing.T) {
	dir := t.TempDir()

	criado, err := GarantirConfigModelo(dir)
	if err != nil || !criado {
		t.Fatalf("GarantirConfigModelo = %v,%v, quero true,nil", criado, err)
	}
	if err := os.WriteFile(CaminhoConfig(dir), []byte("PAGBANK_TOKEN=preenchido\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	criado, err = GarantirConfigModelo(dir)
	if err != nil || criado {
		t.Fatalf("GarantirConfigModelo = %v,%v, quero false,nil", criado, err)
	}
	b, err := os.ReadFile(CaminhoConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got, quero := string(b), "PAGBANK_TOKEN=preenchido\n"; got != quero {
		t.Errorf("conteúdo = %q, quero %q", got, quero)
	}
}

func TestModeloGeradoEValido(t *testing.T) {
	semAmbiente(t)
	dir := t.TempDir()
	if _, err := GarantirConfigModelo(dir); err != nil {
		t.Fatal(err)
	}
	env, err := config.Load(CaminhoConfig(dir))
	if err != nil {
		t.Fatalf("o modelo não passa pelo config.Load: %v", err)
	}
	if v := env.Get(config.EnvEmail); v != "" {
		t.Errorf("%s = %q, quero vazio", config.EnvEmail, v)
	}
	if _, err := env.Legacy(); err == nil {
		t.Error("o modelo em branco passou por credencial completa")
	}
}

func TestExtrairGravaCSVComDataEHoraNoNome(t *testing.T) {
	o := opcoesDeTeste(t)
	srv, _ := servidor(t, o)
	dir := o.Dir
	comConfig(t, dir)

	resp := pedirExtracao(t, srv, "2026-08-01", "2026-08-31")
	eventos := lerAte(t, resp.Body, "fim")

	var fim eventoFim
	if err := json.Unmarshal(ultimo(t, eventos, "fim").dados, &fim); err != nil {
		t.Fatal(err)
	}

	quero := "transacoes-2026-09-01_143022.csv"
	if len(fim.Arquivos) != 1 || fim.Arquivos[0].Nome != quero {
		t.Fatalf("arquivos = %+v, quero um chamado %q", fim.Arquivos, quero)
	}
	if _, err := os.Stat(filepath.Join(dir, quero)); err != nil {
		t.Errorf("o CSV não foi gravado: %v", err)
	}
	if fim.Linhas != 2 {
		t.Errorf("linhas = %d, quero 2", fim.Linhas)
	}
	if fim.Vazio {
		t.Error("vazio = true, quero false")
	}
}

func TestFimCarregaOsAvisos(t *testing.T) {
	o := opcoesDeTeste(t)
	o.Fonte = fabricaDe(&fonteFake{res: &source.Result{
		Tables:   []sheet.Table{tabelaExemplo()},
		Warnings: []string{"o período começa antes do limite de 6 meses da API legada"},
	}})
	srv, _ := servidor(t, o)
	dir := o.Dir
	comConfig(t, dir)

	resp := pedirExtracao(t, srv, "2026-08-01", "2026-08-31")
	eventos := lerAte(t, resp.Body, "fim")

	var fim eventoFim
	if err := json.Unmarshal(ultimo(t, eventos, "fim").dados, &fim); err != nil {
		t.Fatal(err)
	}
	if len(fim.Avisos) != 1 {
		t.Errorf("avisos = %v, quero um", fim.Avisos)
	}
	if fim.Pasta != dir {
		t.Errorf("pasta = %q, quero %q", fim.Pasta, dir)
	}
}

func TestProgressoDetalheVemComFeitosETotal(t *testing.T) {
	o := opcoesDeTeste(t)
	o.Fonte = fabricaDe(&fonteFake{durante: func(logf func(string, ...any), progresso func(int, int)) {
		logf("janela %s, página %d/%d: %d transações", "2026-08-01 a 2026-08-31", 1, 1, 2)
		progresso(1, 2)
		progresso(2, 2)
	}})
	srv, _ := servidor(t, o)
	dir := o.Dir
	comConfig(t, dir)

	resp := pedirExtracao(t, srv, "2026-08-01", "2026-08-31")
	eventos := lerAte(t, resp.Body, "fim")

	var comTotal, comTexto int
	for _, e := range eventos {
		if e.nome != "progresso" {
			continue
		}
		var p eventoProgresso
		if err := json.Unmarshal(e.dados, &p); err != nil {
			t.Fatal(err)
		}
		if p.Total > 0 {
			comTotal++
		}
		if p.Texto != "" {
			comTexto++
		}
	}
	if comTotal != 2 {
		t.Errorf("%d evento(s) com total, quero 2", comTotal)
	}
	if comTexto != 1 {
		t.Errorf("%d evento(s) com texto, quero 1", comTexto)
	}
}

func TestSegredoNaoVazaNoProgresso(t *testing.T) {
	o := opcoesDeTeste(t)
	o.Fonte = fabricaDe(&fonteFake{durante: func(logf func(string, ...any), _ func(int, int)) {
		// O legacy repassa o texto do Logf sem redigir; se um formato novo lá
		// dentro levar a URL da API, o token vem na query string.
		logf("GET https://ws.pagseguro.uol.com.br/v2/transactions?token=%s", testSenha)
	}})
	srv, _ := servidor(t, o)
	dir := o.Dir
	comConfig(t, dir)

	resp := pedirExtracao(t, srv, "2026-08-01", "2026-08-31")
	eventos := lerAte(t, resp.Body, "fim")

	var redigiu bool
	for _, e := range eventos {
		if strings.Contains(string(e.dados), testSenha) {
			t.Fatalf("o token vazou no evento %q: %s", e.nome, e.dados)
		}
		if strings.Contains(string(e.dados), "***") {
			redigiu = true
		}
	}
	if !redigiu {
		t.Error("nenhum evento trouxe o marcador de redação")
	}
}

func TestSemTokenDeSessaoResponde403(t *testing.T) {
	srv, _ := servidor(t, opcoesDeTeste(t))

	for _, caminho := range []string{"/", "/vivo", "/extrair", "/?t=errado"} {
		resp, err := http.Get(srv.URL + caminho)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s = %d, quero %d", caminho, resp.StatusCode, http.StatusForbidden)
		}
	}
}

func TestHostForaDoLoopbackResponde403(t *testing.T) {
	srv, _ := servidor(t, opcoesDeTeste(t))

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/?t="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	// É o que chega num DNS rebinding: o nome resolve para 127.0.0.1, mas o Host
	// carrega o domínio que atraiu o navegador.
	req.Host = "malicioso.example:8080"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("host de fora = %d, quero %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestOrigemCruzadaResponde403(t *testing.T) {
	srv, _ := servidor(t, opcoesDeTeste(t))

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/?t="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("origem cruzada = %d, quero %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestSegundaExtracaoSimultaneaAvisa(t *testing.T) {
	entrou, liberar := make(chan struct{}), make(chan struct{})
	o := opcoesDeTeste(t)
	o.Fonte = fabricaDe(&fonteFake{entrou: entrou, liberar: liberar})
	srv, _ := servidor(t, o)
	dir := o.Dir
	comConfig(t, dir)

	primeira := pedirExtracao(t, srv, "2026-08-01", "2026-08-31")
	<-entrou

	segunda := pedirExtracao(t, srv, "2026-08-01", "2026-08-31")
	var e eventoErro
	if err := json.Unmarshal(ultimo(t, lerAte(t, segunda.Body, "erro"), "erro").dados, &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Mensagem, "já existe uma extração em andamento") {
		t.Errorf("mensagem = %q, quero avisar da extração em andamento", e.Mensagem)
	}

	close(liberar)
	lerAte(t, primeira.Body, "fim")
}

func TestDataFinalAnteriorNaoCitaFlag(t *testing.T) {
	o := opcoesDeTeste(t)
	srv, _ := servidor(t, o)
	dir := o.Dir
	comConfig(t, dir)

	resp := pedirExtracao(t, srv, "2026-08-31", "2026-08-01")
	var e eventoErro
	if err := json.Unmarshal(ultimo(t, lerAte(t, resp.Body, "erro"), "erro").dados, &e); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.Mensagem, "--from") || strings.Contains(e.Mensagem, "--to") {
		t.Errorf("mensagem = %q, quero sem vocabulário de flag", e.Mensagem)
	}
	if !strings.Contains(e.Mensagem, "data final") {
		t.Errorf("mensagem = %q, quero falar da data final", e.Mensagem)
	}
}

func TestExtrairSemCredencialAvisa(t *testing.T) {
	srv, _ := servidor(t, opcoesDeTeste(t))

	resp := pedirExtracao(t, srv, "2026-08-01", "2026-08-31")
	var e eventoErro
	if err := json.Unmarshal(ultimo(t, lerAte(t, resp.Body, "erro"), "erro").dados, &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Mensagem, config.EnvEmail) {
		t.Errorf("mensagem = %q, quero citar %s", e.Mensagem, config.EnvEmail)
	}
}

func TestFecharAAbaCancelaAExtracao(t *testing.T) {
	entrou, cancelo := make(chan struct{}), make(chan struct{})
	o := opcoesDeTeste(t)
	o.Fonte = fabricaDe(&fonteFake{entrou: entrou, liberar: make(chan struct{}), cancelo: cancelo})
	srv, _ := servidor(t, o)
	dir := o.Dir
	comConfig(t, dir)

	ctx, cancelar := context.WithCancel(context.Background())
	q := url.Values{"t": {testToken}, "de": {"2026-08-01"}, "ate": {"2026-08-31"}, "detalhes": {"1"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/extrair?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	<-entrou
	cancelar()

	select {
	case <-cancelo:
	case <-time.After(5 * time.Second):
		t.Fatal("a extração não foi cancelada quando a aba fechou")
	}

	// Nada foi gravado: a escrita só começa depois que o Fetch inteiro retorna.
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entradas {
		if strings.HasSuffix(e.Name(), ".csv") {
			t.Errorf("%s ficou no disco depois do cancelamento", e.Name())
		}
	}
}

func TestWatchdogAvisaQuandoNinguemAbreAPagina(t *testing.T) {
	avisos := make(chan string, 1)
	o := opcoesDeTeste(t)
	o.URL = "http://127.0.0.1:1234/?t=" + testToken
	o.Avisar = func(_, texto string) { avisos <- texto }
	o.Encerrar = func() { t.Error("o watchdog encerrou o programa em vez de só avisar") }
	o.EsperaPrimeiroCliente = time.Millisecond

	s := New(o)
	defer s.Parar()

	select {
	case texto := <-avisos:
		if !strings.Contains(texto, "127.0.0.1:1234") {
			t.Errorf("aviso = %q, quero conter a URL", texto)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("o watchdog não avisou")
	}
}

func TestWatchdogEncerraDepoisQueOClienteSai(t *testing.T) {
	encerrar, encerrou := encerrador()
	o := opcoesDeTeste(t)
	o.Encerrar = encerrar
	o.EsperaSemCliente = time.Millisecond
	srv, _ := servidor(t, o)

	ctx, cancelar := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/vivo?t="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancelar()
	resp.Body.Close()

	select {
	case <-encerrou:
	case <-time.After(5 * time.Second):
		t.Fatal("o watchdog não encerrou depois que a última aba saiu")
	}
}

func TestBotaoEncerrarDesligaOPrograma(t *testing.T) {
	encerrar, encerrou := encerrador()
	o := opcoesDeTeste(t)
	o.Encerrar = encerrar
	srv, _ := servidor(t, o)

	resp, err := http.Post(srv.URL+"/sair?t="+testToken, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST /sair = %d, quero %d", resp.StatusCode, http.StatusNoContent)
	}

	select {
	case <-encerrou:
	case <-time.After(5 * time.Second):
		t.Fatal("o botão não encerrou o programa")
	}
}

func TestDuracaoEmPortugues(t *testing.T) {
	casos := []struct {
		d     time.Duration
		quero string
	}{
		{3 * time.Second, "3 s"},
		{90 * time.Second, "1 min 30 s"},
		{4*time.Minute + 12*time.Second, "4 min 12 s"},
		{2 * time.Minute, "2 min"},
	}
	for _, c := range casos {
		if got := duracaoEmPortugues(c.d); got != c.quero {
			t.Errorf("duracaoEmPortugues(%s) = %q, quero %q", c.d, got, c.quero)
		}
	}
}

// Os três testes abaixo prendem o mesmo defeito, que já apareceu de verdade: o
// processo do Chromium que lançamos morre em milissegundos quando ele precisa
// criar o perfil, recuperar um perfil sujo ou repassar a linha de comando para
// uma instância existente. Desligar o servidor nessa hora deixa a janela abrir
// em cima de um "não consigo chegar a esta página".

func TestSemJanelaNaoEncerraAntesDaPaginaAbrir(t *testing.T) {
	o := opcoesDeTeste(t)
	o.Encerrar = func() { t.Error("encerrou antes de a página abrir alguma vez") }
	s := New(o)
	defer s.Parar()

	s.SemJanela()
	time.Sleep(20 * o.EsperaSemJanela)
}

func TestSemJanelaNaoEncerraComAPaginaAberta(t *testing.T) {
	o := opcoesDeTeste(t)
	o.Encerrar = func() { t.Error("encerrou com a página aberta") }
	srv, s := servidor(t, o)

	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/vivo?t="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// O processo que lançamos morreu, mas quem abriu a janela foi outro.
	s.SemJanela()
	time.Sleep(20 * o.EsperaSemJanela)
}

func TestSemJanelaEncerraDepoisQueAPaginaFechou(t *testing.T) {
	encerrar, encerrou := encerrador()
	o := opcoesDeTeste(t)
	o.Encerrar = encerrar
	o.EsperaSemCliente = time.Hour // só o SemJanela pode encerrar aqui
	srv, s := servidor(t, o)

	ctx, cancelar := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/vivo?t="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancelar()
	resp.Body.Close()

	s.SemJanela()
	select {
	case <-encerrou:
	case <-time.After(5 * time.Second):
		t.Fatal("não encerrou depois de a janela fechar com a página já fechada")
	}
}

func TestWatchdogVoltaDepoisDeAdiarPorExtracao(t *testing.T) {
	// Sem janela e sem página, mas com uma extração ainda se desfazendo, o
	// watchdog adia — e precisa voltar. Sem o reagendamento o processo ficaria
	// órfão para sempre: invisível, sem janela e segurando a porta.
	encerrar, encerrou := encerrador()
	o := opcoesDeTeste(t)
	o.Encerrar = encerrar
	o.EsperaSemJanela = 10 * time.Millisecond
	s := New(o)
	defer s.Parar()

	s.extraindo.Store(true)
	s.talvezEncerrar()

	select {
	case <-encerrou:
		t.Fatal("encerrou com uma extração em curso")
	case <-time.After(20 * o.EsperaSemJanela):
	}

	s.extraindo.Store(false)
	select {
	case <-encerrou:
	case <-time.After(5 * time.Second):
		t.Fatal("o watchdog não voltou depois que a extração terminou: processo órfão")
	}
}
