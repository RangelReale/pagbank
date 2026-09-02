// pagbank-extract baixa a movimentação de uma conta empresarial PagBank e
// grava em planilhas CSV.
//
// Uso:
//
//	pagbank-extract edi        --from 2026-08-01 [--to 2026-08-31] [--types ...]
//	pagbank-extract transacoes --from 2026-08-01 [--to 2026-08-31]
//	pagbank-extract version
//
// As credenciais vêm do ambiente ou de um arquivo .env; veja o README.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"time"

	"github.com/RangelReale/pagbank/internal/config"
	"github.com/RangelReale/pagbank/internal/httpx"
	"github.com/RangelReale/pagbank/internal/sheet"
	"github.com/RangelReale/pagbank/internal/source"
	"github.com/RangelReale/pagbank/internal/source/edi"
	"github.com/RangelReale/pagbank/internal/source/legacy"
)

const version = "1.0.0"

func main() {
	// Ctrl+C cancela a extração no meio; o que já foi baixado não é gravado
	// pela metade porque a gravação só começa depois que a busca termina.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\ninterrompido")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "erro: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage(os.Stderr)
		return errors.New("informe um comando")
	}

	switch cmd := args[0]; cmd {
	case "edi":
		return runEDI(ctx, args[1:])
	case "transacoes", "transactions":
		return runLegacy(ctx, args[1:])
	case "version", "--version", "-version":
		fmt.Println(versionString())
		return nil
	case "help", "--help", "-h":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("comando %q não existe", cmd)
	}
}

// opts são as flags comuns aos dois comandos de extração.
type opts struct {
	from    string
	to      string
	out     string
	envFile string
	verbose bool
}

func (o *opts) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.from, "from", "", "data inicial, AAAA-MM-DD (obrigatório)")
	fs.StringVar(&o.to, "to", "", "data final, AAAA-MM-DD (padrão: hoje)")
	fs.StringVar(&o.out, "out", "saida", "diretório onde gravar os CSVs")
	fs.StringVar(&o.envFile, "env", ".env", "arquivo com as credenciais")
	fs.BoolVar(&o.verbose, "v", false, "mostra o progresso requisição a requisição")
}

// period valida as datas e monta o período a extrair.
func (o *opts) period() (source.Period, error) {
	if o.from == "" {
		return source.Period{}, errors.New("informe --from (data inicial, AAAA-MM-DD)")
	}
	from, err := source.ParseDate(o.from)
	if err != nil {
		return source.Period{}, fmt.Errorf("--from: %w", err)
	}
	to := time.Now()
	if o.to != "" {
		if to, err = source.ParseDate(o.to); err != nil {
			return source.Period{}, fmt.Errorf("--to: %w", err)
		}
	}
	return source.NewPeriod(from, to)
}

// logger devolve a função de progresso, ou nil quando -v não foi pedido.
func (o *opts) logger() func(string, ...any) {
	if !o.verbose {
		return nil
	}
	return func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "  "+format+"\n", args...)
	}
}

func runEDI(ctx context.Context, args []string) error {
	var o opts
	var tipos string
	var pageSize int

	fs := flag.NewFlagSet("edi", flag.ContinueOnError)
	o.bind(fs)
	fs.StringVar(&tipos, "types", strings.Join(edi.MovementTypes, ","), "tipos de movimento, separados por vírgula")
	fs.IntVar(&pageSize, "page-size", edi.DefaultPageSize, "registros por requisição (máximo 1000)")
	fs.Usage = func() { usageEDI(fs) }
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, err := o.period()
	if err != nil {
		return err
	}
	lista := splitList(tipos)
	if err := edi.ValidateTypes(lista); err != nil {
		return err
	}

	env, err := config.Load(o.envFile)
	if err != nil {
		return err
	}
	cred, err := env.EDI()
	if err != nil {
		return err
	}

	hc := httpx.New(httpx.NewRedactor(cred.Secrets()...))
	hc.Logf = o.logger()

	c := edi.New(cred, hc)
	c.Types = lista
	c.PageSize = pageSize
	c.Logf = o.logger()

	return extrair(ctx, c, p, o)
}

func runLegacy(ctx context.Context, args []string) error {
	var o opts
	var semDetalhes, taxasDetalhadas bool

	fs := flag.NewFlagSet("transacoes", flag.ContinueOnError)
	o.bind(fs)
	fs.BoolVar(&semDetalhes, "sem-detalhes", false, "pula o detalhe de cada transação: fica muito mais rápido, mas parcelas, itens, última atualização, fim da retenção e o detalhe do meio de pagamento saem em branco")
	fs.BoolVar(&taxasDetalhadas, "taxas-detalhadas", false, "acrescenta quatro colunas com a taxa aberta (tarifa fixa de intermediação, intermediação, parcelamento e operacional); depende do detalhe, então não vale com --sem-detalhes")
	fs.Usage = func() { usageLegacy(fs) }
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, err := o.period()
	if err != nil {
		return err
	}

	env, err := config.Load(o.envFile)
	if err != nil {
		return err
	}
	cred, err := env.Legacy()
	if err != nil {
		return err
	}

	hc := httpx.New(httpx.NewRedactor(cred.Secrets()...))
	hc.Logf = o.logger()

	c := legacy.New(cred, hc)
	c.SemDetalhes = semDetalhes
	c.TaxasDetalhadas = taxasDetalhadas
	c.Logf = o.logger()

	return extrair(ctx, c, p, o)
}

// extrair roda a fonte e grava as tabelas, relatando o que aconteceu.
func extrair(ctx context.Context, s source.Source, p source.Period, o opts) error {
	fmt.Fprintf(os.Stderr, "extraindo %s de %s...\n", s.Name(), p)
	inicio := time.Now()

	res, err := s.Fetch(ctx, p)
	if err != nil {
		return err
	}

	var arquivos []string
	for _, t := range res.Tables {
		path, err := sheet.WriteFile(o.out, t, sheet.DefaultOptions())
		if err != nil {
			return err
		}
		arquivos = append(arquivos, path)
		vazio := ""
		if t.Empty() {
			vazio = " (sem registros)"
		}
		fmt.Fprintf(os.Stderr, "  %s: %s linha(s)%s\n", path, milhar(len(t.Rows)), vazio)
	}

	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "aviso: %s\n", w)
	}
	fmt.Fprintf(os.Stderr, "%s dia(s), %s registro(s), %d arquivo(s) em %s (%s)\n",
		milhar(p.Len()), milhar(res.Records()), len(arquivos), o.out, time.Since(inicio).Round(time.Second))
	return nil
}

// splitList separa uma lista por vírgula descartando itens vazios.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// milhar formata um inteiro com ponto de milhar, como se lê em português.
func milhar(n int) string {
	s := fmt.Sprint(n)
	sinal := ""
	if strings.HasPrefix(s, "-") {
		sinal, s = "-", s[1:]
	}
	var b strings.Builder
	for i, d := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(d)
	}
	return sinal + b.String()
}

func versionString() string {
	v := "pagbank-extract " + version
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				v += " (" + s.Value[:7] + ")"
			}
		}
	}
	return v
}

func usage(w *os.File) {
	fmt.Fprintf(w, `pagbank-extract — extrai a movimentação de uma conta empresarial PagBank para CSV.

Uso:
  pagbank-extract <comando> [flags]

Comandos:
  edi         extrato completo pela API do Extrato EDI: transações, movimentação
              financeira, saques e saldos. Exige o EDI ativado na conta.
  transacoes  vendas pela API legada de transações. O token sai no painel do
              vendedor, mas ela só enxerga vendas dos últimos 6 meses.
  version     mostra a versão
  help        mostra esta ajuda

Credenciais (no ambiente ou em um arquivo .env, nunca por flag):
  %s / %s   para o comando edi
  %s / %s        para o comando transacoes

Exemplos:
  pagbank-extract edi --from 2026-08-01 --to 2026-08-31
  pagbank-extract edi --from 2026-08-01 --types financial,balances --out ./agosto
  pagbank-extract transacoes --from 2026-08-01 -v
  pagbank-extract transacoes --from 2026-08-01 --sem-detalhes
  pagbank-extract transacoes --from 2026-08-01 --taxas-detalhadas

Rode "pagbank-extract <comando> -h" para as flags de cada comando.
Como obter cada credencial: veja o README.md.
`, config.EnvEDIUser, config.EnvEDIToken, config.EnvEmail, config.EnvToken)
}

func usageEDI(fs *flag.FlagSet) {
	fmt.Fprintf(fs.Output(), `Uso: pagbank-extract edi --from AAAA-MM-DD [flags]

Extrai o extrato EDI, um arquivo CSV por tipo de movimento. A API entrega uma
data por requisição, então o período é percorrido dia a dia, e os dados de um dia
só são garantidos em D+1.

Tipos de movimento (--types):
`)
	for _, t := range edi.MovementTypes {
		fmt.Fprintf(fs.Output(), "  %-14s %s\n", t, edi.Descriptions[t])
	}
	fmt.Fprintf(fs.Output(), "\nFlags:\n")
	fs.PrintDefaults()
	fmt.Fprintf(fs.Output(), "\nCredenciais: %s e %s.\n", config.EnvEDIUser, config.EnvEDIToken)
}

func usageLegacy(fs *flag.FlagSet) {
	fmt.Fprintf(fs.Output(), `Uso: pagbank-extract transacoes --from AAAA-MM-DD [flags]

Extrai as vendas pela API legada, em um único CSV. A API limita cada consulta a
%d dias e guarda %d meses de histórico; períodos maiores são fatiados
automaticamente.

A consulta por data devolve só um resumo de cada transação: parcelas, itens,
última atualização, fim da retenção e o detalhe do meio de pagamento não vêm
nela. Por isso cada transação é buscada também pelo código, o que custa uma
requisição a mais por transação — conte alguns minutos num mês de muitas vendas.
Com --sem-detalhes essa segunda passada é pulada e essas cinco colunas saem em
branco.

Flags:
`, legacy.MaxWindowDays, legacy.MaxHistoryMonths)
	fs.PrintDefaults()
	fmt.Fprintf(fs.Output(), "\nCredenciais: %s e %s.\n", config.EnvEmail, config.EnvToken)
}
