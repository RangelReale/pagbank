// PagBank-Extrator é a interface web local do comando transacoes: abre o
// navegador numa página com dois campos de data e grava a planilha de vendas na
// pasta do próprio executável.
//
// É feito para ser aberto com um duplo clique no Explorer, por quem não usa
// terminal. Por isso o binário é compilado com -ldflags="-H=windowsgui": não há
// janela de console. A consequência é que nada escrito em os.Stdout ou
// os.Stderr aparece a ninguém — o que precisa ser dito antes de o navegador
// estar de pé sai por avisar, que no Windows é uma caixa de mensagem nativa.
//
// As credenciais vêm de um config.env ao lado do executável, no mesmo formato
// do .env da linha de comando. Quem quiser as flags e o extrato EDI continua
// usando o pagbank-extract.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/RangelReale/pagbank/internal/web"
)

const (
	version = "1.0.0"
	titulo  = "PagBank Extrator"
	// esperaDoDesligamento é quanto o servidor dá às requisições em curso antes
	// de cortar. Depois de cancelar o contexto base não sobra nada demorado.
	esperaDoDesligamento = 5 * time.Second
	// esperaSemJanelaDeApp é o ocioso do watchdog quando a interface abriu em
	// janela própria. Curto porque ali a página é a única cliente possível, mas
	// não instantâneo: um F5 derruba a conexão por um instante.
	esperaSemJanelaDeApp = 8 * time.Second
)

func main() {
	dir := flag.String("dir", "", "pasta onde ficam o config.env e os CSVs (padrão: a do executável; serve para desenvolvimento e para rodar de uma pasta somente leitura)")
	flag.Parse()

	err := run(opcoes{
		dir:   *dir,
		abrir: abrirJanela,
		// Com -H=windowsgui o stdout não vai a lugar nenhum, mas quem rodar o
		// programa com a saída redirecionada — o suporte, um script — recebe o
		// endereço mesmo assim.
		pronto: func(u string) { fmt.Println(u) },
	})
	if err != nil {
		avisar(titulo, err.Error())
		os.Exit(1)
	}
}

// opcoes é o que o main injeta e o teste ponta a ponta substitui: sem isso não
// há como exercitar o programa inteiro sem abrir o navegador de quem roda os
// testes.
type opcoes struct {
	dir    string
	abrir  func(url, perfil string) (*janela, error)
	pronto func(url string)
}

func run(o opcoes) error {
	// Fechar a janela ou um Ctrl+C, quando houver console, encerram tudo.
	ctx, pararSinal := signal.NotifyContext(context.Background(), os.Interrupt)
	defer pararSinal()

	pasta, err := pastaDeTrabalho(o.dir)
	if err != nil {
		return err
	}

	// Descobrir agora que a pasta não aceita gravação é muito melhor do que
	// descobrir depois de gastar minutos numa extração. O programa continua: a
	// página explica o problema e o caminho, que é mais legível que uma caixa
	// de mensagem sozinha.
	erroPasta := ""
	if err := web.PastaGravavel(pasta); err != nil {
		erroPasta = fmt.Sprintf("Não consigo gravar em %s: %v", pasta, err)
	}

	criado := false
	if erroPasta == "" {
		if criado, err = web.GarantirConfigModelo(pasta); err != nil {
			return fmt.Errorf("não consegui criar o %s em %s:\n\n%v", web.NomeConfig, pasta, err)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("não consegui abrir a porta local:\n\n%v", err)
	}
	defer ln.Close()

	token := web.NovoToken()
	endereco := fmt.Sprintf("http://%s/?t=%s", ln.Addr().String(), url.QueryEscape(token))

	// O contexto base é o que desliga os fluxos SSE. Shutdown espera as
	// conexões ativas terminarem, e um SSE nunca termina sozinho: sem cancelar
	// este contexto antes, o desligamento ficaria pendurado para sempre.
	baseCtx, fecharFluxos := context.WithCancel(context.Background())
	defer fecharFluxos()

	ctxServidor, pedirDesligamento := context.WithCancel(context.Background())
	var uma sync.Once
	encerrar := func() { uma.Do(pedirDesligamento) }

	// Numa janela de aplicativo a página é a única cliente que vai existir, e o
	// usuário não tem outras abas nossas abertas: se ela some, ele fechou a
	// janela. A folga do watchdog pode ser bem menor que a do navegador comum,
	// onde a aba pode estar só recarregando ou ter sido movida de janela.
	espera := time.Duration(0)
	if navegadorDeApp() != "" {
		espera = esperaSemJanelaDeApp
	}

	s := web.New(web.Opcoes{
		Dir:          pasta,
		Token:        token,
		URL:          endereco,
		Versao:       versionString(),
		ErroPasta:    erroPasta,
		ModeloCriado: criado,
		Encerrar:     encerrar,
		Avisar:       avisar,

		EsperaSemCliente: espera,
	})
	defer s.Parar()

	srv := &http.Server{
		Handler:           s,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// WriteTimeout fica zerado de propósito: /extrair é um fluxo que dura o
		// tempo inteiro da extração, e um prazo global o cortaria no meio.
	}

	if o.pronto != nil {
		o.pronto(endereco)
	}
	go func() {
		// Falhar aqui não é fatal: passados vinte segundos sem ninguém abrir a
		// página, o próprio servidor mostra o endereço numa caixa de mensagem.
		j, err := o.abrir(endereco, perfilDaJanela())
		if err != nil || j == nil || j.Cmd == nil {
			return
		}
		// Fechar a janela é o gesto natural de fechar um programa, e esperar o
		// processo do navegador é o jeito de perceber isso na hora.
		//
		// Mas o fim desse processo NÃO prova que a janela fechou: o Chromium sai
		// e volta sozinho ao criar o perfil, ao se recuperar de um perfil sujo e
		// ao repassar a linha de comando para uma instância existente. Quem
		// decide é o SemJanela, que só desiste se a página tiver aberto antes e
		// não estiver aberta agora.
		_ = j.Cmd.Wait()
		s.SemJanela()
	}()

	erros := make(chan error, 1)
	go func() { erros <- srv.Serve(ln) }()

	select {
	case err := <-erros:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("o servidor local parou:\n\n%v", err)
		}
	case <-ctx.Done():
	case <-ctxServidor.Done():
	}

	fecharFluxos()
	desligamento, cancelar := context.WithTimeout(context.Background(), esperaDoDesligamento)
	defer cancelar()
	if err := srv.Shutdown(desligamento); err != nil {
		srv.Close()
	}
	return nil
}

// pastaDeTrabalho resolve onde o programa procura o config.env e grava os CSVs.
//
// É a pasta do próprio executável, não o diretório de trabalho: o Explorer
// define o diretório de trabalho a partir do "Iniciar em" do atalho, e "executar
// como administrador" costuma trocá-lo por C:\Windows\system32. O usuário
// raciocina sobre "a pasta onde está o programa", e é essa que precisa valer.
//
// Sob `go run` o executável fica no cache de build, numa pasta temporária que
// some no fim — por isso o override existe.
func pastaDeTrabalho(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("não consegui descobrir onde este programa está:\n\n%v", err)
	}
	// Resolver o link importa em instalações por symlink; falhar não é motivo
	// para desistir, o caminho não resolvido serve.
	if resolvido, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolvido
	}
	return filepath.Dir(exe), nil
}

func versionString() string {
	v := version
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				v += " (" + s.Value[:7] + ")"
			}
		}
	}
	return v
}
