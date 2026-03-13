![agent client protocol golang banner](./imgs/banner-dark.jpg)

# Agent Client Protocol - Go 구현체

Agent Client Protocol (ACP)의 Go 구현체입니다. ACP는 _코드 에디터_(소스 코드를 보고 편집하는 대화형 프로그램)와 _코딩 에이전트_(생성형 AI를 사용하여 자율적으로 코드를 수정하는 프로그램) 간의 통신을 표준화합니다.

이것은 Go로 작성된 ACP 사양의 **비공식** 구현체입니다. 공식 프로토콜 사양과 참조 구현체는 [공식 저장소](https://github.com/zed-industries/agent-client-protocol)에서 찾을 수 있습니다.

> [!NOTE]
> Agent Client Protocol은 활발히 개발 중입니다. 이 구현체는 최신 사양 변경사항을 따라가지 못할 수 있습니다. 가장 최신의 프로토콜 사양은 [공식 저장소](https://github.com/zed-industries/agent-client-protocol)를 참조해 주세요.

프로토콜에 대한 자세한 내용은 [agentclientprotocol.com](https://agentclientprotocol.com/)에서 확인하세요.

## 설치

```bash
go get github.com/ironpark/go-acp
```

## 기능

- **JSON-RPC 2.0** 양방향 통신
- **플러그형 Transport** - stdio, HTTP+SSE 지원
- **미들웨어** - 로깅, 복구, 타임아웃 등 요청 처리 체인
- **SessionStream** - 세션 업데이트 전송 편의 API
- **Match 패턴** - 제네릭 기반 discriminated union 패턴 매칭
- **세션 관리** - 다중 동시 세션 지원
- **파일 시스템 작업** - 텍스트 파일 읽기/쓰기
- **터미널 작업** - 생성, 출력, 대기, 종료, 해제
- **연결 안정성** - 요청 타임아웃, 쓰기 큐 설정, 그레이스풀 셧다운

## 빠른 시작

### 에이전트 구현

```go
package main

import (
    "context"
    "os"

    acp "github.com/ironpark/go-acp"
)

type MyAgent struct{}

func (a *MyAgent) Initialize(ctx context.Context, params *acp.InitializeRequest) (*acp.InitializeResponse, error) {
    return &acp.InitializeResponse{
        ProtocolVersion:   acp.ProtocolVersion(acp.CurrentProtocolVersion),
        AgentCapabilities: &acp.AgentCapabilities{},
    }, nil
}

// ... 다른 Agent 인터페이스 메서드 구현

func main() {
    agent := &MyAgent{}
    conn := acp.NewAgentSideConnection(agent, os.Stdin, os.Stdout,
        acp.WithMiddleware(acp.RecoveryMiddleware()),
    )
    conn.Start(context.Background())
}
```

### SessionStream 사용

에이전트에서 세션 업데이트를 보낼 때 보일러플레이트를 줄여줍니다:

```go
stream := acp.NewSessionStream(client, sessionID)

// 텍스트 스트리밍
stream.SendText(ctx, "안녕하세요!")
stream.SendThought(ctx, "생각 중...")

// 도구 호출 라이프사이클
stream.StartToolCall(ctx, toolID, "파일 읽기", acp.ToolKindRead)
stream.CompleteToolCall(ctx, toolID, content...)
stream.FailToolCall(ctx, toolID)
```

### Match 패턴

Discriminated union 타입에 대한 완전한 패턴 매칭:

```go
acp.MatchSessionUpdate(&update, acp.SessionUpdateMatcher[string]{
    AgentMessageChunk: func(v acp.SessionUpdateAgentMessageChunk) string {
        if text, ok := v.Content.AsText(); ok {
            return text.Text
        }
        return ""
    },
    ToolCall: func(v acp.SessionUpdateToolCall) string {
        return v.Title
    },
    Default: func() string { return "" },
})
```

### 미들웨어

요청 처리 체인에 횡단 관심사를 추가합니다:

```go
conn := acp.NewAgentSideConnection(agent, reader, writer,
    acp.WithMiddleware(
        acp.RecoveryMiddleware(),                 // 패닉 복구
        acp.LoggingMiddleware(log.Printf),        // 메서드 호출 로깅
        acp.TimeoutMiddleware(30 * time.Second),  // 요청별 타임아웃
    ),
)
```

### HTTP+SSE Transport

HTTP를 통한 에이전트 배포:

```go
// 서버 (에이전트)
transport := acp.NewHTTPServerTransport()
conn := acp.NewConnection(handler, nil, nil, acp.WithTransport(transport))
http.Handle("/", transport.Handler())

// 클라이언트
transport := acp.NewHTTPClientTransport("http://localhost:8080")
transport.Connect(ctx)
conn := acp.NewConnection(handler, nil, nil, acp.WithTransport(transport))
```

## 아키텍처

- **`Connection`**: 동시 요청/응답 상관관계를 처리하는 양방향 전송 계층
- **`Transport`**: 플러그형 전송 인터페이스 (stdio, HTTP+SSE)
- **`AgentSideConnection`**: 에이전트 구현을 위한 고수준 ACP 인터페이스
- **`ClientSideConnection`**: 클라이언트 구현을 위한 고수준 ACP 인터페이스
- **`SessionStream`**: 세션 업데이트 전송 편의 래퍼
- **`Middleware`**: 조합 가능한 요청/응답 처리 체인
- **`TerminalHandle`**: 터미널 세션 리소스 관리 래퍼

## 프로토콜 지원

이 구현체는 ACP 프로토콜 버전 1을 지원합니다:

### 에이전트 메서드 (클라이언트 → 에이전트)
- `initialize` - 에이전트 초기화 및 기능 협상
- `authenticate` - 에이전트 인증 (선택사항)
- `session/new` - 새로운 대화 세션 생성
- `session/load` - 기존 세션 로드 (지원하는 경우)
- `session/list` - 세션 목록 조회
- `session/set_mode` - 세션 모드 변경
- `session/set_config_option` - 세션 설정 업데이트
- `session/prompt` - 사용자 프롬프트를 에이전트에 전송
- `session/cancel` - 진행 중인 작업 취소

### 클라이언트 메서드 (에이전트 → 클라이언트)
- `session/update` - 세션 업데이트 전송 (알림)
- `session/request_permission` - 작업에 대한 사용자 권한 요청
- `fs/read_text_file` - 클라이언트 파일시스템에서 텍스트 파일 읽기
- `fs/write_text_file` - 클라이언트 파일시스템에 텍스트 파일 쓰기
- **터미널** (불안정):
  - `terminal/create`, `terminal/output`, `terminal/wait_for_exit`, `terminal/kill`, `terminal/release`

### 불안정 기능
- `session/fork` - 세션 포크 (`SessionForker` 인터페이스)
- `session/resume` - 세션 재개 (`SessionResumer` 인터페이스)
- `session/close` - 세션 닫기 (`SessionCloser` 인터페이스)
- `session/set_model` - 모델 설정 (`ModelSetter` 인터페이스)

### 연결 옵션

```go
acp.NewConnection(handler, reader, writer,
    acp.WithWriteQueueSize(500),                    // 쓰기 큐 크기 설정
    acp.WithRequestTimeout(30 * time.Second),       // 기본 요청 타임아웃
    acp.WithShutdownTimeout(10 * time.Second),      // 그레이스풀 셧다운 타임아웃
    acp.WithErrorHandler(func(err error) { ... }),   // 에러 콜백
)
```

## 예제

완전한 작동 예제는 [docs/example](./example/) 디렉토리를 참조하세요:

- **[에이전트 예제](./example/agent/)** - SessionStream, 미들웨어, 권한 요청을 활용한 에이전트 구현
- **[클라이언트 예제](./example/client/)** - SpawnAgent와 MatchSessionUpdate를 활용한 클라이언트 구현

## 개발

### 빌드

```bash
go build ./...
```

### 테스트

```bash
go test ./...
```

## 기여하기

이것은 비공식 구현체입니다. 프로토콜 사양 변경사항은 [공식 저장소](https://github.com/zed-industries/agent-client-protocol)에 기여해 주세요.

Go 구현체 이슈 및 개선사항은 이슈를 열거나 풀 리퀘스트를 보내주세요.

## 라이선스

이 구현체는 공식 ACP 사양과 동일한 라이선스를 따릅니다.

## 관련 프로젝트

- **공식 ACP 저장소**: [zed-industries/agent-client-protocol](https://github.com/zed-industries/agent-client-protocol)
- **Rust 구현체**: 공식 저장소의 일부
- **프로토콜 문서**: [agentclientprotocol.com](https://agentclientprotocol.com/)

### ACP를 지원하는 에디터

- [Zed](https://zed.dev/docs/ai/external-agents)
- [neovim](https://neovim.io) - [CodeCompanion](https://github.com/olimorris/codecompanion.nvim) 플러그인을 통해
- [yetone/avante.nvim](https://github.com/yetone/avante.nvim): Cursor AI IDE의 동작을 에뮬레이트하도록 설계된 Neovim 플러그인
