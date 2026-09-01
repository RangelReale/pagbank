package web

import (
	"embed"
	"html/template"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/RangelReale/pagbank/internal/config"
	"github.com/RangelReale/pagbank/internal/source"
	"github.com/RangelReale/pagbank/internal/source/legacy"
)

//go:embed index.html
var arquivos embed.FS

// modelos é html/template, e não text/template, porque a página interpola
// caminho de arquivo e texto de erro: o escape automático é o que separa isso de
// uma injeção de marcação.
var modelos = template.Must(template.ParseFS(arquivos, "*.html"))

// dadosPagina é tudo que a página precisa saber. Uma página só, com dois
// estados: falta credencial, ou dá para extrair.
type dadosPagina struct {
	Token  string
	Pasta  string
	Versao string

	// Configurar liga o estado "falta credencial".
	Configurar   bool
	Config       string // caminho completo do config.env
	ModeloCriado bool
	Faltando     []string
	Modelo       []string
	ComoObter    []string
	// AmbienteVence avisa que uma variável de ambiente está sobrepondo o
	// arquivo, que é uma pegadinha de config.Get difícil de descobrir sozinho.
	AmbienteVence bool
	ErroPasta     string

	// Datas do formulário, todas em AAAA-MM-DD, que é o que o input de data usa.
	De  string
	Ate string
	Min string
	Max string
}

// pagina serve a interface. O estado é recalculado a cada requisição — inclusive
// a leitura do config.env —, então recarregar depois de preencher o arquivo
// basta, sem reiniciar o programa.
func (s *Servidor) pagina(w http.ResponseWriter, r *http.Request) {
	hoje := s.o.Agora()
	d := dadosPagina{
		Token:  s.o.Token,
		Pasta:  s.o.Dir,
		Versao: s.o.Versao,
		Config: CaminhoConfig(s.o.Dir),

		De:  time.Date(hoje.Year(), hoje.Month(), 1, 0, 0, 0, 0, hoje.Location()).Format(source.DateLayout),
		Ate: hoje.Format(source.DateLayout),
		// O limite de histórico da API vira limite do seletor de datas: o
		// navegador impede o pedido impossível antes de a API precisar recusá-lo.
		Min: hoje.AddDate(0, -legacy.MaxHistoryMonths, 0).Format(source.DateLayout),
		Max: hoje.Format(source.DateLayout),

		ErroPasta: s.o.ErroPasta,
	}

	if _, faltando, err := s.credenciais(); err != nil {
		d.Configurar = true
		d.Faltando = faltando
		d.ModeloCriado = s.o.ModeloCriado
		d.Modelo = linhasDoModelo()
		d.ComoObter = ComoObterOToken
		_, d.AmbienteVence = os.LookupEnv(config.EnvToken)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := modelos.ExecuteTemplate(w, "index.html", d); err != nil {
		// O cabeçalho já foi enviado; não há como devolver um 500 útil.
		return
	}
}

// renderRecusa é a página de quem chegou sem a chave da execução.
func renderRecusa(w io.Writer) {
	io.WriteString(w, `<!doctype html><html lang="pt-BR"><meta charset="utf-8">
<title>Endereço sem a chave</title>
<style>body{font:16px/1.6 system-ui,sans-serif;max-width:34rem;margin:4rem auto;padding:0 1rem;color:#1c1c1c}</style>
<h1>Endereço sem a chave</h1>
<p>Cada execução do programa gera uma chave própria, e o endereço precisa
carregá-la. Isso impede que outra página aberta no seu navegador converse com
este programa.</p>
<p>Volte à aba que o programa abriu sozinho. Se ela não existe mais, feche o
programa e abra de novo.</p>
</html>`)
}
