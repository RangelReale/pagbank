# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## O projeto

`pagbank-extract` é um CLI em Go que baixa a movimentação de uma conta empresarial PagBank e a
grava em CSVs prontos para abrir no Excel em português. Duas fontes independentes, escolhidas pelo
subcomando: `edi` (extrato completo, JSON) e `transacoes` (só vendas, XML, API legada).

Há um segundo executável, `cmd/pagbank-web`, que expõe **só a fonte `transacoes`** numa página
local, para quem não abre terminal. Ele é um consumidor dos mesmos pacotes internos, não uma
segunda implementação.

**Leia o [README.md](README.md) antes de mexer em qualquer fonte.** Ele documenta o que cada API
cobre, como obter cada credencial, o formato do CSV, os avisos que a aplicação emite e os dois
pontos que a documentação pública do PagBank não resolve. Este arquivo não repete nada disso.

## Comandos

```sh
task                 # compila os dois executáveis em saida/
task build           # só o pagbank-extract[.exe] da linha de comando
task build:web       # só a interface web: saida/PagBank-Extrator[.exe]
task run:web         # compila e abre a interface web na pasta atual
task test            # go test ./...
task test:update     # go test ./... -update — regrava os CSVs golden
task vet             # go vet ./...  (é o único "lint"; não há linter nem CI)
task run -- transacoes --from 2026-08-25 -v    # compila e executa
task --list          # tudo o que existe
```

Sem o [Task](https://taskfile.dev), o equivalente em Go direto: `go build ./cmd/pagbank-extract`,
`go test ./...`, `go vet ./...`.

Um teste só, ou um pacote só:

```sh
go test ./internal/source/legacy -run TestFetchPaginaEDeduplica -v
go test ./internal/source/edi -run 'TestFetch.*' -v
go test ./internal/sheet -update      # regrava só os golden desse pacote
```

`gofmt -l .` lista todos os arquivos numa cópia Windows: o `core.autocrlf` deixa os `.go` com CRLF
no disco e o gofmt quer LF. Não é desformatação — compare ignorando o `\r` antes de "arrumar" nada.

## Restrições que não se negociam

- **Só biblioteca padrão.** O `go.mod` não tem um único `require`, não há `go.sum`, e deve
  continuar assim — é o que permite compilar num clone novo sem rede. Antes de alcançar uma
  dependência, escreva o pedaço que falta: é o que `internal/httpx`, `internal/config` e o
  `latin1Reader` de `legacy.go` são.
- **Go 1.26.** `errors.AsType` (`internal/httpx/httpx.go:222`) e `math/rand/v2` não existem antes.
- **Credencial nunca entra por flag** — só pelo ambiente ou pelo `.env`, via `internal/config`. A
  linha de comando fica no histórico do shell e na lista de processos.
- **Todo segredo passa pelo `httpx.Redactor`** antes de chegar a log, erro ou saída. A API legada
  carrega o token na *query string*, então uma URL crua numa mensagem de erro vaza a credencial.
  Erro novo que leve URL ou corpo de resposta: redija. `Redactor.Error` não encadeia com `%w` de
  propósito — o erro original guarda o texto cru e um `%w` o traria de volta à tona.
- **Nenhum teste toca a rede.** Tudo é `httptest` mais fixture em `testdata/`, com o relógio fixado
  pelo campo `Now` do cliente. Em ponta a ponta, aponte `PAGBANK_EDI_BASE_URL` ou
  `PAGBANK_LEGACY_BASE_URL` para um servidor local.
- **A interface web nunca sai do loopback.** `net.Listen("tcp", "127.0.0.1:0")`, porta sorteada,
  mais um token por execução na query string (`internal/web/seguranca.go`). Ligar em `0.0.0.0`
  exporia a movimentação da conta na rede local e ainda dispararia o alerta do firewall do Windows,
  que o loopback não dispara.
- **Todo texto que chega ao navegador passa pelo `Redactor`.** O `httpx` redige as linhas dele, mas
  `legacy.Client.logf` repassa o texto cru; a redação da interface acontece num lugar só, no `logf`
  montado em `internal/web/web.go`, para que um formato novo em `legacy` não vire vazamento na tela.
- **O binário da web não tem console.** Compilado com `-ldflags="-H=windowsgui"`, um
  `fmt.Fprintln(os.Stderr, ...)` ali não falha — simplesmente não aparece a ninguém. A única saída
  fora do navegador é `avisar`, que no Windows é um `MessageBoxW`
  (`cmd/pagbank-web/aviso_windows.go`).
- **Valor monetário nunca passa por `float`.** É transcrito como texto da API até o CSV:
  `json.Number.String()` no EDI, `decimalComma` em `internal/sheet/csv.go` na gravação.

## Idioma

Prosa, comentários, doc comments, mensagens ao usuário, textos de erro, nomes de teste e mensagens
de commit em **português**. Nos testes, `quero` é o "want" do idioma local — as asserções se leem
`t.Errorf("%s = %q, quero %q", ...)`.

Os identificadores Go seguem o costume de cada arquivo: a API genérica e o que espelha campo de API
ficam em inglês (`Table`, `Column`, `Kind`, `Source`, `Fetch`, `Result`, `MaxWindowDays`), os termos
do domínio e do fluxo em português (`SemDetalhes`, `tipos`, `meiosDePagamento`, `buscar`,
`detalhar`, `estadoDia`, `milhar`, `saida`). As flags misturam pelo mesmo critério: `--from`,
`--to`, `--out`, `--types`, mas `--sem-detalhes`.

Os doc comments explicam *por que* a solução é aquela, quase sempre citando o comportamento da API
que a forçou — `internal/sheet/sheet.go` e `internal/httpx/httpx.go` são a régua. Vale manter.

## Arquitetura

O fluxo é uma linha reta, sem estado global:

```
cmd/pagbank-extract  → flags, credenciais, escolha da fonte, relatório final
cmd/pagbank-web      → pasta do exe, servidor em 127.0.0.1, janela de app, watchdog
  internal/web       → rotas, página embutida, progresso por SSE  (só transacoes)
  internal/source    → contrato Source{Name, Fetch(ctx, Period)} → Result{Tables, Warnings}
    .../edi          → um GET por (tipo, dia, página); JSON de layout não publicado
    .../legacy       → janelas de 30 dias → páginas → 2ª passada de detalhe por transação
  internal/sheet     → Builder → Table → CSV
```

Invariantes que só aparecem lendo vários arquivos:

- **As fontes não compartilham schema.** Cada uma monta as próprias tabelas e nada dos campos de
  origem é descartado. Não tente unificar: o layout do EDI v3.00 não é documentado, e é por isso
  que `sheet.Builder` deixa uma coluna *surgir* na segunda página — `Builder.Column` a cria no ato
  e preenche com vazio as linhas anteriores, e rebaixa para `KindText` se o mesmo cabeçalho chegar
  com `Kind` conflitante.
- **A célula é canônica, não localizada.** O `Kind` diz o que ela guarda: `KindNumber` é
  `"-1234.56"`, `KindDate` é `"2006-01-02"`. Ponto e vírgula brasileiros, `dd/mm/aaaa`, BOM UTF-8 e
  CRLF entram só em `csv.go`, na gravação. Nunca formate para pt-BR dentro de uma fonte.
- **String parecida com número continua texto** (`normalizeString`, em `edi/json.go`). NSU, BIN e
  código de autorização são identificadores: tratá-los como número comeria o zero à esquerda.
- **Aviso ao usuário não é erro.** Dado parcial (`VALIDADO: FALSE`), dia sem movimento (404),
  detalhe que falhou, duplicata entre páginas, período cortado no presente: viram `Result.Warnf` e
  a extração continua. Só o que invalida a planilha inteira volta como `error`.
- **A gravação começa depois que a busca termina** (`extrair`, em `main.go`), para que um Ctrl+C no
  meio não deixe CSV pela metade.
- **O resumo é autoritativo sobre o detalhe.** `merge`, em `legacy.go`, só preenche campo vazio: um
  detalhe parcial nunca apaga o que a busca por data trouxe. E detalhe que falha não derruba a
  extração — a linha fica com o resumo e o total de falhas vira aviso.
- **A pasta da interface web é a do executável, não o diretório de trabalho.** No Explorer o
  diretório de trabalho vem do "Iniciar em" do atalho, e "executar como administrador" o troca por
  `system32`. `pastaDeTrabalho`, em `cmd/pagbank-web/main.go`, usa `os.Executable`.
- **O `config.env` é o `.env` em outro caminho.** Mesmo formato, mesmo `config.Load`, mesmo
  `Env.Legacy()`. O nome muda porque um arquivo só com extensão some no Explorer. Ele é relido a
  cada requisição, não uma vez na subida: o usuário preenche o arquivo com o programa já rodando.
- **Uma extração por vez na interface web.** O espaçamento do `httpx` é um mutex por cliente, então
  duas extrações simultâneas se estrangulariam e dobrariam o risco de bater no limite de taxa.
- **O CSV da web é carimbado com data e hora** (`sheet.FileNameAt`) e nunca sobrescreve: todas as
  extrações caem na mesma pasta, e o carimbo também evita esbarrar numa planilha aberta no Excel.
- **Coluna de código cru é autoritativa.** As traduções de `status`, `type` e meio de pagamento são
  conveniência de leitura, gravadas *ao lado* do código, em coluna própria.

## Armadilhas da API legada (já resolvidas — não desfaça)

- `Accept: application/xml;charset=ISO-8859-1` é obrigatório nos **dois** endpoints. O RESTEasy
  antigo que serve essa API compara o media type com os parâmetros, e `application/xml` puro
  devolve 406. Pinado por `TestFetchEnviaAcceptComCharset` e
  `TestFetchDetalhesEnviaAcceptComCharset`.
- `finalDate` é cortado em *agora*, não no fim do dia, menos um minuto de `clockSkew`: o resto de
  hoje ainda é futuro e a API recusa a consulta inteira com o código `13009`.
- As datas vão no fuso de Brasília, fixo (`var brasilia`), porque a API não aceita deslocamento na
  query e o Brasil não tem horário de verão desde 2019.

## Armadilhas da interface web (já resolvidas — não desfaça)

- **O fim do processo do navegador NÃO prova que a janela fechou.** O Chromium sai e volta sozinho
  ao criar o perfil, ao se recuperar de um perfil sujo e ao repassar a linha de comando para uma
  instância existente: nesses casos o processo que lançamos morre em milissegundos e a janela é
  aberta por outro. Encerrar o servidor no `Wait` derrubava tudo antes de a página carregar, e a
  janela abria em cima de um "não consigo chegar a esta página" — aconteceu de verdade. Por isso o
  `Wait` chama `Servidor.SemJanela`, que só desiste se a página tiver aberto ao menos uma vez e o
  prazo vencer sem ninguém conectado. Pinado por `TestSemJanelaNaoEncerraAntesDaPaginaAbrir` e
  `TestSemJanelaNaoEncerraComAPaginaAberta`.
- **O `--user-data-dir` da janela não é conforto.** Sem ele, com o Edge já aberto, a janela de
  `--app` é adotada pelo processo existente e o `Start` volta na hora, sem nada para acompanhar — e
  o programa perde como saber que a janela fechou. Com um perfil próprio o Chromium não reaproveita
  a instância, o processo é só nosso, e o `Wait` em `main.go` faz fechar a janela encerrar o
  programa na hora. De quebra, isola a sessão do usuário.
- **A busca pelo Chromium é por variável de ambiente, nunca por caminho absoluto.** O Windows pode
  estar em outra unidade, numa instalação de 32 bits não existe `ProgramFiles(x86)`, e tanto o Edge
  quanto o Chrome podem estar em `%LocalAppData%` (instalação por usuário). Ver `chromiums`, em
  `cmd/pagbank-web/navegador.go`.
- **O watchdog continua mesmo com o `Wait`.** Ele é a rede de segurança do degrau que não tem
  processo próprio — o navegador padrão —, e do caso em que o navegador morre deixando a página
  órfã, ou o contrário.
- **`Shutdown` fica pendurado num fluxo SSE.** Ele espera as conexões ativas terminarem, e um SSE
  não termina sozinho. Por isso o `http.Server` tem um `BaseContext` próprio, cancelado *antes* do
  `Shutdown` (`cmd/pagbank-web/main.go`): cancelar o contexto base derruba os contextos das
  requisições em voo. Pinado por `TestProgramaGeraOCSVPontaAPonta`.
- **`WriteTimeout` cortaria a extração no meio.** Fica zerado de propósito: `/extrair` é um fluxo
  que dura o tempo inteiro da busca.
- **`EventSource` reconecta sozinho.** Sem `es.close()` nos eventos terminais, uma queda de conexão
  dispararia uma extração nova a cada tentativa. O `index.html` fecha o fluxo no `fim` e no `erro`.
- **Recusa vai pelo fluxo, não como status.** Um `EventSource` que recebe status diferente de 200 só
  dispara `onerror`, sem corpo: o 409 de "já existe uma extração" viraria uma tela muda. Por isso
  `/extrair` abre o fluxo e manda a recusa como evento `erro`.
- **Cookie não serve de sessão aqui.** Cookie de `http://127.0.0.1` ignora a porta, então outro
  programa local servindo em outra porta leria e sobrescreveria o nosso. O token vai na query
  string, que também é o único lugar de onde o `EventSource` consegue mandá-lo.
- **O `sources:` do `build:web` inclui os `.html`.** A página é embutida com `//go:embed`; sem o
  glob, editá-la não invalida a impressão digital do Task e o build devolve o binário velho em
  silêncio. E o `.gitattributes` marca `internal/web/*.html` com `-text`, senão o `core.autocrlf`
  faria os bytes embutidos mudarem com o sistema de quem compilou.

## Códigos do PagBank (`internal/source/legacy/codigos.go`)

Os mapas ali só recebem código **confirmado numa conta real** — as tabelas que circulam em SDKs de
terceiros divergem entre si, e rotular uma linha errada é pior que deixá-la com `código N`. Ao
acrescentar um, registre no comentário de onde veio a confirmação, como os códigos `8` e `11` já
fazem. É a única mudança necessária: `describe` cuida do resto.

## Os CSVs golden

`internal/*/testdata/*.csv` são bytes exatos, com BOM e CRLF — e por isso o `.gitattributes` marca
esses caminhos com `-text`, para o git não normalizar as quebras de linha. CSV novo de teste vai sob
um `testdata/`, senão o `.gitignore` (que ignora `*.csv`) o engole.

**Nunca rode `-update` para fazer um golden que falha passar.** O `-update` regrava o arquivo a
partir do código sob teste, e o teste passa a comparar a saída consigo mesma. Leia o diff primeiro:
o golden é o que prova que o CSV continua abrindo no Excel com um duplo clique.

## Três arquivos locais com dado real

Todos ignorados pelo git, todos fora dos testes — não leia, não cole em resposta, não versione:

- `.env` — as credenciais de verdade da conta.
- `config.env` — as mesmas credenciais, ao lado do executável da interface web.
- `saida/*.csv` — a saída de extrações reais, com códigos e valores de transações verdadeiras.

As fixtures e os golden vêm de dados sintéticos, e é assim que devem continuar.
