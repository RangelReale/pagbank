# pagbank-extract

Extrai a movimentação de uma conta empresarial PagBank para planilhas CSV, prontas para abrir
no Excel em português.

Escrito em Go, só com a biblioteca padrão — não há dependências para instalar.

```
go build ./cmd/pagbank-extract
```

Com o [Task](https://taskfile.dev) instalado, `task build` faz o mesmo e grava o
executável em `saida/`.

## Qual fonte usar

O PagBank não tem uma API única de "extrato da conta". Existem duas fontes viáveis, com
credenciais, cobertura e limites bem diferentes:

| | `edi` | `transacoes` |
|---|---|---|
| API | [Extrato EDI v3.00](https://developer.pagbank.com.br/docs/api-do-extrato-edi) | [transações v2 (legada)](https://developer.pagbank.com.br/v1/reference/api-checkout-transparente-consulta-transacoes-por-data-ou-codigo-de-referencia) |
| Cobre | transações, movimentação financeira, saques e saldos | só vendas |
| Histórico | desde a ativação do EDI | últimos 6 meses |
| Atualidade | D-1 (o dia fecha no dia seguinte) | tempo real |
| Credencial | chamado no Pipefy, chega por e-mail | gerada na hora, no painel |
| Formato | JSON | XML |

**Para conferência contábil e conciliação, use `edi`** — é a única fonte que traz a liquidação
dos recebíveis, os saques e os saldos. O `transacoes` serve como acesso imediato enquanto o EDI
não é liberado, ou para conferir vendas do dia corrente, que no EDI ainda não fecharam.

## Credenciais

Ficam no ambiente ou em um arquivo `.env` no diretório de trabalho — **nunca em flag**, que
apareceria no histórico do shell e na lista de processos. Copie `.env.example` para `.env` e
preencha o que for usar; o `.gitignore` já ignora o `.env`.

A interface web lê as mesmas variáveis, no mesmo formato, de um arquivo chamado `config.env` ao
lado do executável — um nome que aparece no Explorer, que esconde as extensões e engoliria um
arquivo chamado só `.env`.

### Credenciais do EDI

Para `pagbank-extract edi`, duas variáveis:

```
PAGBANK_EDI_USER=<número do estabelecimento>
PAGBANK_EDI_TOKEN=<token da API EDI>
```

Como obter, segundo a [documentação do EDI](https://developer.pagbank.com.br/docs/edi):

1. A conta precisa ser de vendedor e estar ativa.
2. Abra um chamado no [portal de ativação (Pipefy)](https://app.pipefy.com/organizations/142456/interfaces/3668b8ed-d930-4bcf-8038-8a00d3ed6901)
   escolhendo **"Novas Ativações - EDI"**.
3. No chamado, escolha o modelo de token:
   - **1xN** — um único token para vários estabelecimentos;
   - **1x1** — um token por estabelecimento.
4. O token chega no e-mail cadastrado. O `PAGBANK_EDI_USER` é o número do estabelecimento.

Não existe sandbox do EDI. Dúvidas de ativação: `edi@pagbank.com.br`.

### Credenciais do painel

Para `pagbank-extract transacoes`, duas variáveis:

```
PAGBANK_EMAIL=<e-mail de acesso à conta>
PAGBANK_TOKEN=<token da API, gerado no painel>
```

O token é gerado no painel do vendedor, em Preferências → Integrações → Token de segurança.
**Não é a senha da conta.**

## Para quem não usa terminal

Existe um segundo executável, `PagBank-Extrator.exe`, que faz o mesmo que
`pagbank-extract transacoes` numa página no navegador — duas datas e um botão.
É o que se entrega a quem só quer a planilha.

```
task build:web        # gera saida/PagBank-Extrator.exe
```

Na máquina de quem vai usar, basta o executável — não há nada para instalar:

1. Copie o `.exe` para uma pasta sua (a Área de Trabalho ou Documentos serve).
   Não deixe em `Arquivos de Programas` nem numa pasta de rede: ele grava as
   planilhas ao lado de si mesmo e precisa de permissão de escrita.
2. Dê um duplo clique. **Não abre janela de terminal** nem aba no seu navegador:
   o programa aparece numa janela própria, sem barra de endereço e sem abas, com
   ícone na barra de tarefas.
3. Na primeira vez a página vai dizer que faltam as credenciais e um arquivo
   `config.env` terá aparecido na mesma pasta, em branco e comentado. Abra,
   preencha `PAGBANK_EMAIL` e `PAGBANK_TOKEN` (o mesmo token do painel descrito
   acima), salve e recarregue a página.
4. Escolha as datas e clique em **Gerar planilha**. O CSV aparece na pasta do
   executável, com a data e a hora no nome —
   `transacoes-2026-09-01_143022.csv` —, então uma extração nunca apaga a
   anterior nem esbarra numa planilha ainda aberta no Excel.
5. Para fechar, feche a janela no X, ou clique em **Encerrar o programa**. Nos
   dois casos o programa sai na hora.

A janela é desenhada pelo Microsoft Edge, que já vem instalado no Windows 10 e
no 11 — não há nada a instalar. Numa máquina sem Edge, o programa tenta o Chrome
e, na falta dos dois, abre uma aba no navegador padrão. Ele não mexe na sua
sessão do Edge: usa um perfil separado, em
`%LocalAppData%\PagBank-Extrator`, que não aparece no seu histórico nem entre as
suas abas. Essa pasta pode ser apagada a qualquer momento; ela nasce de novo na
execução seguinte.

Na primeira execução o Windows pode mostrar **"O Windows protegeu o seu PC"**:
o executável não é assinado. Clique em *Mais informações* e depois em *Executar
assim mesmo*.

### O que a interface web não faz

Só a fonte `transacoes`. Para o extrato EDI, os quatro CSVs, o `--types` e o
resto das flags, use o `pagbank-extract` na linha de comando.

### Segurança

O servidor escuta só em `127.0.0.1`, nunca na rede local, e cada execução sorteia
uma chave que precisa estar no endereço — sem ela o programa responde 403, para
que nenhuma outra página aberta no seu navegador consiga falar com ele. Nada é
enviado para fora: as únicas requisições que saem da máquina são as do próprio
PagBank.

O `config.env` é tão sensível quanto uma senha. Não o deixe numa pasta
sincronizada (OneDrive, Google Drive) nem o envie por e-mail.

## Uso

```sh
# Extrato completo de agosto, um CSV por tipo de movimento
pagbank-extract edi --from 2026-08-01 --to 2026-08-31

# Só a movimentação financeira e os saldos, em outro diretório
pagbank-extract edi --from 2026-08-01 --types financial,balances --out ./agosto

# Vendas dos últimos dias pela API legada, com progresso
pagbank-extract transacoes --from 2026-08-25 -v

# Só o resumo das vendas, sem a segunda passada de detalhe — bem mais rápido
pagbank-extract transacoes --from 2026-08-25 --sem-detalhes
```

`--to` é opcional e vale hoje. `--out` é opcional e vale `saida/`.

Os arquivos gerados:

| Comando | Arquivos |
|---|---|
| `edi` | `transactional.csv`, `financial.csv`, `cashouts.csv`, `balances.csv` |
| `transacoes` | `transacoes.csv` |
| interface web | `transacoes-<data>_<hora>.csv`, na pasta do executável |

Rode `pagbank-extract help`, ou `pagbank-extract edi -h`, para todas as flags.

### O detalhe das transações

A consulta por data da API legada devolve um **resumo** de cada transação. Quatro campos não
vêm nela e só existem em `/v2/transactions/{código}`:

| Coluna do CSV | Campo da API |
|---|---|
| Última atualização | `lastEventDate` |
| Meio de pagamento (detalhe) | `paymentMethod/code` |
| Parcelas | `installmentCount` |
| Itens | `itemCount` |

Por isso o `transacoes` busca cada transação uma segunda vez, pelo código — é o que preenche
essas colunas. Custa uma requisição a mais por transação, espaçadas para não esbarrar no limite
da API: conte alguns minutos num mês de muitas vendas.

`--sem-detalhes` pula essa segunda passada. A extração fica na velocidade de uma requisição por
página de mil transações, e as quatro colunas acima saem em branco — com um aviso dizendo isso.

Uma transação cujo detalhe falhe não derruba a extração nem some do CSV: a linha fica com o que
o resumo trouxe, e o total de falhas vira aviso no fim.

(A coluna `Referência` também costuma sair vazia, mas isso não é limitação da consulta: venda de
maquininha não tem código de pedido externo.)

## O CSV

Formatado para o Excel em português: separador `;`, vírgula decimal, datas `dd/mm/aaaa`,
BOM UTF-8 (sem ele o Excel corrompe os acentos) e fim de linha `\r\n`. Basta abrir com um
duplo clique — não precisa passar pelo assistente de importação.

Valores monetários são transcritos **exatamente** como a API os devolve, sem passar por
ponto flutuante, para não introduzir erro de arredondamento em centavos.

## Duas coisas para conferir com dados reais

O código foi escrito a partir da documentação pública, que é incompleta em dois pontos. Ambos
foram tratados de forma a não inventar dado nenhum, mas valem uma conferência na primeira
extração de verdade:

**1. Escala dos valores do EDI.** O layout do JSON do EDI v3.00 não é publicado, então a
aplicação transcreve cada campo como veio. Layouts EDI costumam expressar dinheiro em centavos
inteiros. Se na primeira extração os valores vierem 100× maiores que o esperado, é isso —
avise e a conversão entra como uma lista de campos a dividir, em vez de um palpite aplicado a
tudo.

**2. Tabela de `<type>` das transações.** No comando `transacoes`, os códigos de `status` e de
meio de pagamento ganham uma coluna de descrição ao lado da coluna com o código cru. Já a
tabela do campo `type` não consta mais da documentação pública e as listas que circulam em SDKs
de terceiros divergem entre si, então só entra no mapa `tipos` de
[`internal/source/legacy/codigos.go`](internal/source/legacy/codigos.go) o que for confirmado
numa conta real; o resto aparece como `código N`, em vez de arriscar rotular a linha errada.

Da primeira extração real saiu o código `1` (vendas comuns, em crédito, débito e PIX) e, na
tabela de meios de pagamento, os códigos `8` (cartão de débito) e `11` (PIX), que também não
constam da documentação. Ao confirmar outros, acrescente-os no mapa — é a única mudança
necessária.

Em ambos os casos **a coluna com o valor cru é a autoritativa**: nada é descartado.

## Avisos que a aplicação emite

- `VALIDADO=FALSE` — o PagBank ainda estava processando aquele dia. Os dados de um dia só são
  garantidos em D+1; reextraia no dia seguinte para conferir.
- `sem extrato disponível (HTTP 404)` — normalmente é dia sem movimento, mas é também o que a
  API responde para data anterior à ativação do EDI.
- `transações repetidas descartadas` — a paginação da API legada corre sobre dados vivos, e a
  mesma transação pode reaparecer em outra página. A deduplicação é pelo código da transação.
- `sem o detalhe das transações, as colunas ... saem em branco` — `--sem-detalhes` foi usado.
- `N transação(ões) ficaram sem detalhe` — a consulta do detalhe falhou para essas; as linhas
  ficaram com os dados do resumo. Rodar de novo costuma resolver.
- `o período termina no futuro` — a API legada não responde por datas futuras, e o resto do dia
  de hoje é futuro: a consulta é cortada no instante atual. Rodar de novo mais tarde traz as
  vendas que faltavam.

## Desenvolvimento

```sh
go test ./...                      # tudo offline: httptest e fixtures, nenhum teste toca a rede
go test ./... -update              # regrava os CSVs golden em testdata
go vet ./...
```

Os mesmos comandos pelo Taskfile: `task test`, `task test:update` e `task vet`.
`task build:web` compila a interface web e `task run:web` a abre na pasta atual.
`task --list` mostra tudo o que existe.

Estrutura:

| Pacote | Papel |
|---|---|
| `cmd/pagbank-extract` | linha de comando |
| `cmd/pagbank-web` | a interface web local, sem janela de terminal |
| `internal/web` | servidor em 127.0.0.1, progresso por SSE, página embutida |
| `internal/sheet` | modelo de tabela e escrita do CSV |
| `internal/source/edi` | cliente do Extrato EDI e achatamento do JSON |
| `internal/source/legacy` | cliente da API legada de transações |
| `internal/httpx` | retry, espaçamento de chamadas e redação de segredos |
| `internal/config` | credenciais vindas do ambiente e do `.env` |

Para apontar os testes de ponta a ponta para um servidor local, defina
`PAGBANK_EDI_BASE_URL` ou `PAGBANK_LEGACY_BASE_URL`.
