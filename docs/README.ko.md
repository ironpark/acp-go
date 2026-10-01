![Agent Client Protocol Go 배너](./imgs/banner-dark.jpg)

# Agent Client Protocol for Go

[English](../README.md) | 한국어

Agent Client Protocol(ACP)로 통신하는 코딩 에이전트와 에디터 클라이언트를 만드는 Go SDK입니다.
타입이 지정된 API, 세션 업데이트 스트리밍, 취소, stdio·HTTP·WebSocket 연결을 제공합니다.

ACP의 **비공식** 구현체입니다. ACP v1은 안정 버전이며, ACP v2와 HTTP 전송 사양은 초안입니다.
프로토콜이 계속 바뀌므로 [고정된 업스트림 리비전](../schema/typescript/REVISION)을 기준으로 지원합니다.
프로토콜 자체는 [공식 ACP 문서](https://agentclientprotocol.com/)를 참고하세요.

| 프로토콜 | 스키마 버전 |
| --- | --- |
| ACP v1 | [`1.24.1`](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/schema/v1/CHANGELOG.md) |
| ACP v2 (초안) | [`2.0.0-alpha.7`](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/schema/v2/CHANGELOG.md) |

[빠른 시작](#빠른-시작) · [패키지 선택](#패키지-선택) · [예제](#예제) · [문서](#문서)

## 요구 사항 및 설치

**Go 1.27+**가 필요하며 `encoding/json/v2`를 사용합니다.
사용 중인 Go 모듈에서 다음 명령으로 SDK를 추가하세요.

```bash
go get github.com/ironpark/acp-go
```

## 빠른 시작

API 키나 외부 에이전트 없이, 한 프로세스 안에서 에이전트와 클라이언트를 실행해 보세요.

```bash
git clone https://github.com/ironpark/acp-go.git
cd acp-go
go run ./examples/inprocess
```

예상 출력:

```text
>> hello
<< HELLO
>> same process, no child
<< SAME PROCESS, NO CHILD
```

[전체 예제](../examples/inprocess/main.go)는 `acp1.Pipe`로 양쪽을 연결합니다.
연결한 클라이언트는 에이전트를 초기화하고, 세션을 만든 뒤 프롬프트를 보냅니다.
아래는 그 흐름을 발췌한 코드입니다. `agent`와 `ctx`는 전체 예제의 앞부분에서 생성합니다.

```go
if _, err := agent.Initialize(ctx, &acp1.InitializeRequest{}); err != nil {
    log.Fatal(err)
}
session, err := agent.StartSession(ctx, &acp1.NewSessionRequest{Cwd: "/"})
if err != nil {
    log.Fatal(err)
}
turn, err := session.Prompt(ctx, acp1.TextBlock("hello"))
if err != nil {
    log.Fatal(err)
}
text, _, err := turn.Text()
if err != nil {
    log.Fatal(err)
}
fmt.Println(text) // HELLO
```

**에이전트**를 만들려면 [Echo Agent](../examples/echo/)부터 시작하세요.
`Initialize`, `NewSession`, `Prompt`, `CancelSession`을 구현하고,
세션 불러오기 같은 기능은 선택적 인터페이스로 추가합니다.

**클라이언트**를 만들려면 [Client](../examples/client/)를 참고하세요.
`acp1.SpawnAgent`로 stdio 에이전트를 실행하고, `SessionUpdate`와 `RequestPermission`을 구현해
업데이트와 권한 요청을 처리합니다. 연결 설정과 capability 유도는
[SDK 가이드](guide.ko.md#에이전트와-클라이언트-구현)에서 설명합니다.

## 패키지 선택

ACP v1용 `acp1`부터 시작하고, 필요한 기능에 따라 패키지를 추가하세요.

| 패키지 | 사용하는 경우 |
| --- | --- |
| [`acp1`](../acp1/) | 안정 버전 ACP v1 에이전트나 클라이언트 구현 |
| [`acp2`](../acp2/) | 초안 ACP v2 사용. API가 바뀔 수 있음 |
| [`acp`](../) | 공통 transport, 미들웨어, 에러, 스토어, 타입 있는 확장 설정 |
| [`acphttp`](../acphttp/) | Streamable HTTP·WebSocket으로 원격 에이전트 연결(초안) |
| [`router`](../router/) | 두 버전을 함께 제공하거나 v2 → v1 폴백으로 연결 |
| [`acpmcp`](../acpmcp/) | MCP-over-ACP 연결(별도 모듈, 불안정) |
| [`schema/v1`](../schema/v1/) / [`schema/v2`](../schema/v2/) | 생성된 와이어 타입과 검증 규칙 사용 |

## 예제

만들려는 기능에 맞춰 예제를 선택하세요. 실행 명령과 준비 사항은 [예제 가이드](../examples/README.ko.md)에 있습니다.

| 목적 | 예제 |
| --- | --- |
| 한 프로세스에서 에이전트와 클라이언트 실행 | [In-process](../examples/inprocess/) |
| 가장 작은 에이전트 구현 | [Echo Agent](../examples/echo/) |
| 세션, 모드, 도구, 권한 요청 추가 | [Agent](../examples/agent/) |
| stdio 에이전트와 대화 | [Client](../examples/client/) |
| OpenRouter 모델을 사용하는 코딩 에이전트 구현 | [Open Agent](../examples/open-agent/) |
| HTTP·WebSocket 연결 및 세션 다시 불러오기 | [HTTP Agent](../examples/http-agent/) / [HTTP Client](../examples/http-client/) |
| v1·v2 동시 지원 및 폴백 | [Dual Agent](../examples/dual-agent/) / [Dual Client](../examples/dual-client/) |
| 클라이언트의 MCP 서버를 에이전트에 제공 | [MCP over ACP](../acpmcp/) |

## 문서

- [SDK 가이드](guide.ko.md) — 아키텍처, 에이전트·클라이언트 구현, API 사용법
- [세션과 취소](guide.ko.md#세션-관리) — 세션 상태와 프롬프트 턴 관리
- [업데이트 스트리밍](guide.ko.md#sessionstream) — 텍스트, 도구 호출, 계획 전송
- [전송 계층](guide.ko.md#전송-계층) — stdio, HTTP, WebSocket, 재연결
- [버전 라우팅](guide.ko.md#v1과-v2-동시-지원) — 두 버전 제공 및 폴백 설정
- [프로토콜 지원](protocol-support.ko.md) — 메서드·인터페이스 표와 v1·v2 동작 차이
- [스키마 생성](../schema/README.md) — 업스트림 입력, 재생성 방법, 현재 제약

## 기여하기

Go SDK의 이슈와 풀 리퀘스트를 환영합니다. 프로토콜 사양 변경은
[공식 ACP 저장소](https://github.com/agentclientprotocol/agent-client-protocol)에 제안해 주세요.

저장소 루트에서 메인 모듈을 빌드하고 테스트합니다.

```bash
go build ./...
go test ./...
```

MCP 브리지와 스키마 생성기는 별도 모듈입니다. 각각 `acpmcp/`와 `internal/cmd/schema/`에서 테스트하세요.
자세한 내용은 [MCP 가이드](../acpmcp/README.md)와 [스키마 가이드](../schema/README.md)를 참고하세요.

## 라이선스

라이선스 조건은 [LICENSE](../LICENSE)를 참고하세요.
