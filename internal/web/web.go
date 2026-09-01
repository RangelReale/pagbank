// Package web é a interface local do comando transacoes: um servidor HTTP em
// 127.0.0.1 que serve uma página com dois campos de data e grava o CSV na pasta
// do executável.
//
// Ela existe para quem não abre terminal. Por isso o desenho troca flexibilidade
// por previsibilidade em três pontos: só a fonte transacoes, uma extração de
// cada vez, e as credenciais sempre lidas de um config.env ao lado do programa —
// nunca digitadas num formulário, que é o mesmo motivo de a linha de comando não
// aceitar credencial em flag.
//
// A extração roda no goroutine do próprio handler de /extrair, e não numa tarefa
// de fundo com registro e coleta de órfãs: assim o contexto da requisição já é o
// contexto da extração, e fechar a aba cancela a cadeia inteira até o httpx.
package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RangelReale/pagbank/internal/config"
	"github.com/RangelReale/pagbank/internal/httpx"
	"github.com/RangelReale/pagbank/internal/sheet"
	"github.com/RangelReale/pagbank/internal/source"
	"github.com/RangelReale/pagbank/internal/source/legacy"
)

// Padrões do watchdog. Sem console, um processo esquecido não aparece em lugar
// nenhum além do Gerenciador de Tarefas, e continua segurando a porta.
const (
	// esperaPrimeiroClientePadrao é quanto o programa aguarda alguém abrir a
	// página antes de mostrar a URL numa caixa de mensagem. Não encerra: o
	// usuário ainda pode colar a URL no navegador.
	esperaPrimeiroClientePadrao = 20 * time.Second
	// esperaSemClientePadrao é quanto o servidor sobrevive depois que a última
	// aba fechou. A folga existe porque recarregar a página derruba a conexão
	// por um instante.
	esperaSemClientePadrao = 30 * time.Second
	// intervaloDoBatimento espaça os comentários que mantêm o fluxo /vivo
	// escrevendo — é a escrita que falha quando a aba some.
	intervaloDoBatimento = 15 * time.Second
)

// Pedido é o que a página manda: um período e a escolha de buscar ou não o
// detalhe de cada venda.
type Pedido struct {
	De          string
	Ate         string
	SemDetalhes bool
}

// Fabrica monta a fonte de uma extração. É um campo de Opcoes para que o teste
// injete uma fonte de mentira — nenhum teste deste pacote toca a rede.
type Fabrica func(cred config.Legacy, p Pedido, logf func(string, ...any), progresso func(feitos, total int)) source.Source

// FabricaLegada é a Fabrica de produção.
//
// O httpx.Client é novo a cada extração de propósito: o espaçamento entre
// requisições dele é um mutex por cliente, e reaproveitar um faria execuções
// distintas se estrangularem. Além disso o Redactor nasce dos segredos daquela
// leitura do config.env, que pode ter mudado desde a anterior.
func FabricaLegada(cred config.Legacy, p Pedido, logf func(string, ...any), progresso func(feitos, total int)) source.Source {
	hc := httpx.New(httpx.NewRedactor(cred.Secrets()...))
	hc.Logf = logf

	c := legacy.New(cred, hc)
	c.SemDetalhes = p.SemDetalhes
	c.Logf = logf
	c.ProgressoDetalhe = progresso
	return c
}

// Opcoes reúne o que o servidor precisa do mundo externo.
type Opcoes struct {
	// Dir é a pasta do executável: de onde vem o config.env e onde o CSV é
	// gravado.
	Dir string
	// Token é o token de sessão desta execução.
	Token string
	// URL é o endereço completo, com o token, que a caixa de mensagem mostra
	// quando o navegador não abre sozinho.
	URL string
	// Versao aparece no rodapé da página, para quem pedir suporte.
	Versao string
	// ErroPasta, quando não vazio, explica por que a pasta não aceita gravação.
	ErroPasta string
	// ModeloCriado diz que o config.env acabou de nascer nesta execução.
	ModeloCriado bool

	// Encerrar desliga o servidor: o botão da página e o watchdog.
	Encerrar func()
	// Avisar mostra uma mensagem fora do navegador. No Windows é uma caixa
	// nativa; sem console é a única saída que resta.
	Avisar func(titulo, texto string)

	Fonte Fabrica
	// Agora existe para o teste fixar o carimbo de data e hora do nome do
	// arquivo e as datas padrão do formulário.
	Agora func() time.Time

	EsperaPrimeiroCliente time.Duration
	EsperaSemCliente      time.Duration
}

// Servidor atende a interface web. Use sempre o ponteiro: ele guarda um mutex.
type Servidor struct {
	o   Opcoes
	mux *http.ServeMux

	// extraindo é a guarda de uma extração por vez. Não é só zelo: duas
	// extrações simultâneas dobrariam o risco de bater no limite de taxa da API
	// e se estrangulariam no espaçamento do httpx.
	extraindo atomic.Bool

	mu       sync.Mutex
	clientes int
	primeiro *time.Timer
	ocioso   *time.Timer
}

// New monta o servidor. O handler devolvido serve tudo: página, batimento e
// extração.
func New(o Opcoes) *Servidor {
	if o.Fonte == nil {
		o.Fonte = FabricaLegada
	}
	if o.Agora == nil {
		o.Agora = time.Now
	}
	if o.Avisar == nil {
		o.Avisar = func(string, string) {}
	}
	if o.Encerrar == nil {
		o.Encerrar = func() {}
	}
	if o.EsperaPrimeiroCliente == 0 {
		o.EsperaPrimeiroCliente = esperaPrimeiroClientePadrao
	}
	if o.EsperaSemCliente == 0 {
		o.EsperaSemCliente = esperaSemClientePadrao
	}

	s := &Servidor{o: o, mux: http.NewServeMux()}

	// O {$} evita que a raiz vire um coringa que atende qualquer caminho.
	s.mux.HandleFunc("GET /{$}", s.pagina)
	s.mux.HandleFunc("GET /vivo", s.vivo)
	s.mux.HandleFunc("GET /extrair", s.extrair)
	s.mux.HandleFunc("POST /sair", s.sair)

	s.primeiro = time.AfterFunc(o.EsperaPrimeiroCliente, s.ninguemAbriu)
	return s
}

func (s *Servidor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cabecalhosDeSeguranca(w)
	if r.URL.Path == "/favicon.ico" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.autorizado(r) {
		s.recusar(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// recusar responde a quem chegou sem o token. Para uma navegação vale explicar
// em português, e não devolver um "Forbidden" cru: o caso provável não é ataque,
// é o usuário que digitou 127.0.0.1 na barra de endereços.
func (s *Servidor) recusar(w http.ResponseWriter, r *http.Request) {
	if !aceitaHTML(r) {
		http.Error(w, "endereço sem a chave desta execução", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	renderRecusa(w)
}

// sair desliga o programa a pedido da página.
//
// A resposta sai antes do encerramento, e o encerramento vai para outro
// goroutine: desligar primeiro faria o navegador mostrar "não foi possível
// acessar o site" no lugar do "pode fechar esta aba".
func (s *Servidor) sair(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
	go s.o.Encerrar()
}

// vivo é o batimento: enquanto a página mantiver este fluxo aberto, o watchdog
// sabe que há alguém do outro lado.
func (s *Servidor) vivo(w http.ResponseWriter, r *http.Request) {
	f, ok := novoFluxo(w)
	if !ok {
		http.Error(w, "streaming indisponível", http.StatusInternalServerError)
		return
	}

	s.entrouCliente()
	defer s.saiuCliente()

	t := time.NewTicker(intervaloDoBatimento)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			if err := f.bater(); err != nil {
				return
			}
		}
	}
}

func (s *Servidor) entrouCliente() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientes++
	if s.primeiro != nil {
		s.primeiro.Stop()
		s.primeiro = nil
	}
	if s.ocioso != nil {
		s.ocioso.Stop()
		s.ocioso = nil
	}
}

func (s *Servidor) saiuCliente() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientes--
	if s.clientes <= 0 && s.ocioso == nil {
		s.ocioso = time.AfterFunc(s.o.EsperaSemCliente, s.talvezEncerrar)
	}
}

// talvezEncerrar desliga o programa se ninguém voltou e nada está rodando.
func (s *Servidor) talvezEncerrar() {
	s.mu.Lock()
	vazio := s.clientes <= 0
	s.mu.Unlock()
	if vazio && !s.extraindo.Load() {
		s.o.Encerrar()
	}
}

// ninguemAbriu mostra a URL fora do navegador quando ele não subiu sozinho —
// política de grupo, navegador padrão quebrado, rundll32 bloqueado. O servidor
// continua de pé: a URL colada à mão funciona.
func (s *Servidor) ninguemAbriu() {
	s.o.Avisar("PagBank Extrator",
		"Não consegui abrir o navegador.\n\n"+
			"Copie o endereço abaixo e cole na barra do seu navegador:\n\n"+s.o.URL)
}

// Parar solta os temporizadores do watchdog.
func (s *Servidor) Parar() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range []*time.Timer{s.primeiro, s.ocioso} {
		if t != nil {
			t.Stop()
		}
	}
	s.primeiro, s.ocioso = nil, nil
}

// credenciais relê o config.env a cada requisição, e não uma vez na subida: o
// usuário vai preencher o arquivo com o programa já rodando, e recarregar a
// página tem que bastar.
func (s *Servidor) credenciais() (cred config.Legacy, faltando []string, err error) {
	env, err := config.Load(CaminhoConfig(s.o.Dir))
	if err != nil {
		return config.Legacy{}, nil, err
	}
	if cred, err = env.Legacy(); err == nil {
		return cred, nil, nil
	}
	for _, k := range []string{config.EnvEmail, config.EnvToken} {
		if env.Get(k) == "" {
			faltando = append(faltando, k)
		}
	}
	return config.Legacy{}, faltando, err
}

// extrair roda a extração e transmite o progresso.
func (s *Servidor) extrair(w http.ResponseWriter, r *http.Request) {
	f, ok := novoFluxo(w)
	if !ok {
		http.Error(w, "streaming indisponível", http.StatusInternalServerError)
		return
	}

	// A recusa também sai pelo fluxo, e não como 409: um EventSource que recebe
	// status diferente de 200 só dispara onerror, sem corpo, e a página não teria
	// como dizer ao usuário o que aconteceu.
	if !s.extraindo.CompareAndSwap(false, true) {
		f.enviar("erro", eventoErro{Tipo: "outro",
			Mensagem: "já existe uma extração em andamento; espere ela terminar antes de pedir outra"})
		return
	}
	defer s.extraindo.Store(false)

	s.entrouCliente()
	defer s.saiuCliente()

	err := s.rodar(r.Context(), f, pedidoDaQuery(r))
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// A aba já foi embora; escrever no fluxo não chegaria a ninguém.
	default:
		f.enviar("erro", eventoErro{Tipo: "outro", Mensagem: err.Error()})
	}
}

func pedidoDaQuery(r *http.Request) Pedido {
	q := r.URL.Query()
	return Pedido{
		De:  q.Get("de"),
		Ate: q.Get("ate"),
		// O padrão é buscar o detalhe: a planilha completa é a que o usuário
		// espera, e quem escolhe a rápida escolheu conscientemente.
		SemDetalhes: q.Get("detalhes") == "0",
	}
}

// rodar é a extração propriamente dita. O erro devolvido já está redigido.
func (s *Servidor) rodar(ctx context.Context, f *fluxo, p Pedido) error {
	periodo, err := validar(p)
	if err != nil {
		return err
	}

	cred, _, err := s.credenciais()
	if err != nil {
		return err
	}
	red := httpx.NewRedactor(cred.Secrets()...)

	// Toda linha passa pelo Redactor aqui, num lugar só. O httpx já redige as
	// dele, mas o legacy repassa o texto cru, e um formato novo lá dentro não
	// pode virar vazamento nesta tela.
	logf := func(format string, args ...any) {
		f.enviar("progresso", eventoProgresso{Texto: red.String(fmt.Sprintf(format, args...))})
	}
	progresso := func(feitos, total int) {
		f.enviar("progresso", eventoProgresso{Feitos: feitos, Total: total})
	}

	inicio := time.Now()
	res, err := s.o.Fonte(cred, p, logf, progresso).Fetch(ctx, periodo)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return red.Error(err)
	}

	// A gravação só começa depois que a busca inteira terminou, como na linha de
	// comando: um cancelamento no meio não pode deixar CSV pela metade.
	fim := eventoFim{
		Pasta:   s.o.Dir,
		Linhas:  res.Records(),
		Avisos:  make([]string, 0, len(res.Warnings)),
		Duracao: duracaoEmPortugues(time.Since(inicio)),
		Vazio:   res.Records() == 0,
	}
	quando := s.o.Agora()
	for _, t := range res.Tables {
		nome := sheet.FileNameAt(t.Name, quando)
		caminho, err := sheet.WriteFileAs(s.o.Dir, nome, t, sheet.DefaultOptions())
		if err != nil {
			return red.Error(err)
		}
		fim.Arquivos = append(fim.Arquivos, arquivoGerado{Nome: nome, Caminho: caminho, Linhas: len(t.Rows)})
	}
	for _, a := range res.Warnings {
		fim.Avisos = append(fim.Avisos, red.String(a))
	}
	return f.enviar("fim", fim)
}

// validar checa o período com o vocabulário da página.
//
// source.NewPeriod continua sendo a última palavra, mas a mensagem dele cita
// --from e --to, que aqui não querem dizer nada. Validar antes é mais barato do
// que reescrever a mensagem e não duplica regra nenhuma: o que se checa aqui são
// campos de formulário, não o período.
func validar(p Pedido) (source.Period, error) {
	if p.De == "" {
		return source.Period{}, errors.New("informe a data inicial")
	}
	if p.Ate == "" {
		return source.Period{}, errors.New("informe a data final")
	}
	de, err := source.ParseDate(p.De)
	if err != nil {
		return source.Period{}, errors.New("a data inicial não é uma data válida")
	}
	ate, err := source.ParseDate(p.Ate)
	if err != nil {
		return source.Period{}, errors.New("a data final não é uma data válida")
	}
	if ate.Before(de) {
		return source.Period{}, errors.New("a data final é anterior à data inicial")
	}
	return source.NewPeriod(de, ate)
}

// duracaoEmPortugues escreve o tempo decorrido do jeito que se lê em português.
func duracaoEmPortugues(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	m, seg := int(d/time.Minute), int((d%time.Minute)/time.Second)
	if seg == 0 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d min %d s", m, seg)
}

// aceitaHTML diz se quem pediu era um navegador navegando, e não um fetch.
func aceitaHTML(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept") {
		if strings.Contains(v, "text/html") || strings.Contains(v, "*/*") {
			return true
		}
	}
	return false
}
