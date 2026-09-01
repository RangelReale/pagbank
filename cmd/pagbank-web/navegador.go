package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// larguraDaJanela e alturaDaJanela cabem no formulário inteiro sem rolagem numa
// tela de 768 pixels de altura, que ainda é comum em notebook de escritório.
const (
	larguraDaJanela = "620"
	alturaDaJanela  = "780"
)

// janela é a interface aberta.
//
// Cmd é o processo do navegador em modo aplicativo, ou nil quando foi preciso
// cair no navegador padrão — nesse caso não há processo próprio para acompanhar,
// e o watchdog do internal/web volta a ser o único sinal de que a página fechou.
type janela struct {
	Cmd     *exec.Cmd
	ModoApp bool
}

// abrirJanela mostra a interface na melhor moldura disponível.
//
// O modo aplicativo do Chromium (--app) abre uma janela sem barra de endereço,
// sem abas e com ícone próprio na barra de tarefas: o programa deixa de parecer
// um site aberto no navegador do usuário. É o que dá para fazer sem quebrar a
// regra de não ter dependência — hospedar um WebView2 exigiria uma biblioteca
// externa ou mil linhas de COM escritas à mão, e nem isso dispensaria o motor
// do navegador já instalado na máquina.
//
// Sem nenhum Chromium instalado, o último degrau é o navegador padrão, que é
// como o programa sempre funcionou.
func abrirJanela(endereco, perfil string) (*janela, error) {
	if nav := navegadorDeApp(); nav != "" {
		c := exec.Command(nav, argumentosDeApp(endereco, perfil)...)
		esconderJanela(c)
		if err := c.Start(); err == nil {
			return &janela{Cmd: c, ModoApp: true}, nil
		}
		// Um Chromium que existe mas não sobe não é motivo para desistir: o
		// navegador padrão ainda mostra a página.
	}
	return &janela{}, abrirNoNavegadorPadrao(endereco)
}

// argumentosDeApp monta a linha de comando do modo aplicativo. Separada para o
// teste conferir as flags sem precisar de um navegador instalado.
func argumentosDeApp(endereco, perfil string) []string {
	return []string{
		"--app=" + endereco,
		// O perfil próprio não é conforto, é o que faz o modo aplicativo
		// funcionar como aplicativo. Ver perfilDaJanela.
		"--user-data-dir=" + perfil,
		"--window-size=" + larguraDaJanela + "," + alturaDaJanela,
		// O perfil nasce vazio na primeira execução, e sem estas duas o
		// Chromium abriria as telas de boas-vindas e de navegador padrão por
		// cima da interface.
		"--no-first-run",
		"--no-default-browser-check",
	}
}

// chromiums lista onde procurar, na ordem de preferência: primeiro o Edge, que
// vem no Windows desde a atualização de maio de 2020 e portanto está em
// praticamente toda máquina, depois o Chrome.
//
// Os caminhos saem do ambiente e nunca de um "C:\" escrito à mão: o Windows
// pode estar em outra unidade, e numa instalação de 32 bits não existe
// ProgramFiles(x86). O LocalAppData entra porque tanto o Edge quanto o Chrome
// podem ter sido instalados só para o usuário.
var chromiums = []struct{ base, resto string }{
	{"ProgramFiles(x86)", `Microsoft\Edge\Application\msedge.exe`},
	{"ProgramFiles", `Microsoft\Edge\Application\msedge.exe`},
	{"LocalAppData", `Microsoft\Edge\Application\msedge.exe`},
	{"ProgramFiles(x86)", `Google\Chrome\Application\chrome.exe`},
	{"ProgramFiles", `Google\Chrome\Application\chrome.exe`},
	{"LocalAppData", `Google\Chrome\Application\chrome.exe`},
}

// navegadorDeApp procura um Chromium que aceite --app. Devolve vazio quando não
// há nenhum, e aí a interface abre no navegador padrão.
//
// Fora do Windows a busca não se aplica: o modo aplicativo existe, mas os
// caminhos e a forma de instalar variam demais, e o alvo do programa é o
// Explorer do Windows.
func navegadorDeApp() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	for _, c := range chromiums {
		base := os.Getenv(c.base)
		if base == "" {
			continue
		}
		caminho := filepath.Join(base, c.resto)
		if fi, err := os.Stat(caminho); err == nil && !fi.IsDir() {
			return caminho
		}
	}
	return ""
}

// perfilDaJanela é a pasta de perfil que o modo aplicativo usa.
//
// Ela existe por três motivos, e o primeiro é o que a torna obrigatória:
//
//  1. Dá um processo próprio. Sem --user-data-dir, com o Edge já aberto, a
//     janela nova é adotada pelo processo existente e o Start volta na hora,
//     sem nada para acompanhar — e o programa perde como saber que a janela
//     fechou. Com um perfil próprio o Chromium não reaproveita a instância.
//  2. Isola a sessão do usuário: o programa não aparece no histórico, nos
//     cookies nem entre as abas dele.
//  3. Some com a tela de boas-vindas depois da primeira execução, já que o
//     perfil é reaproveitado.
//
// Fica no cache do usuário, e não na pasta do executável: são dezenas de MB de
// arquivos do Chromium, e aquela pasta é onde o usuário procura as planilhas.
func perfilDaJanela() string {
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, "PagBank-Extrator", "janela")
	}
	return filepath.Join(os.TempDir(), "PagBank-Extrator-janela")
}

// abrirNoNavegadorPadrao é o último degrau.
//
// No Windows é rundll32, e não `cmd /c start`: o start trata o primeiro
// argumento entre aspas como título da janela, quebra no & da query string e
// ainda pisca um console — que é justamente o que este binário existe para
// evitar.
func abrirNoNavegadorPadrao(endereco string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", endereco)
	case "darwin":
		c = exec.Command("open", endereco)
	default:
		c = exec.Command("xdg-open", endereco)
	}
	esconderJanela(c)
	return c.Start()
}
