# 프로토콜 지원

[README](README.ko.md) | [SDK 가이드](guide.ko.md) | [English](protocol-support.md)

ACP v1과 초안 v2의 메서드 및 동작 차이를 정리합니다.
지원 기준은 [`schema/typescript/REVISION`](../schema/typescript/REVISION)입니다.
각 표의 인터페이스는 해당 버전의 `acp1` 또는 `acp2` 패키지에 있습니다.
`필수`는 기본 인터페이스의 메서드이며, `unstable` 표시는 변경될 수 있는 기능입니다.

[버전 비교](#버전-비교) · [v1 에이전트](#에이전트-메서드-클라이언트--에이전트) · [v1 클라이언트](#클라이언트-메서드-에이전트--클라이언트) · [v2](#acp-v2-acp2-초안)

## 버전 비교

| 항목 | ACP v1 | ACP v2 (초안) |
| --- | --- | --- |
| 패키지 | `acp1` | `acp2` |
| 파일·셸 접근 | `fs/*`, `terminal/*` | MCP 경유 |
| 프롬프트 응답 | 턴의 결과 반환 | 메시지 접수 확인 |
| 턴 완료 | 프롬프트 응답 시점 | 에이전트의 idle 상태 보고 |
| 진행 중 추가 프롬프트 | `acp.ErrTurnInProgress`로 거절 | 진행 중인 턴에 합류 |

다음 두 표는 **ACP v1** 기준입니다.

## 에이전트 메서드 (클라이언트 → 에이전트)

| 메서드 | Go 인터페이스 |
| --- | --- |
| `initialize`, `session/new`, `session/prompt`, `session/cancel` | `Agent` (필수) |
| `authenticate` | `Authenticator` |
| `session/load` | `SessionLoader` |
| `session/list` | `SessionLister` |
| `session/delete` | `SessionDeleter` |
| `session/fork` | `SessionForker` (unstable) |
| `session/resume` | `SessionResumer` |
| `session/close` | `SessionCloser` |
| `session/set_mode` | `SessionModeSetter` |
| `session/set_config_option` | `SessionConfigOptionSetter` |
| `providers/list`, `providers/set`, `providers/disable` | `ProviderManager` (unstable) |
| `logout` | `LogoutHandler` |
| `nes/*` | `NesHandler` (unstable) |
| `document/did*` | `DocumentHandler` (unstable) |
| `mcp/message` (알림) | `MCPMessageHandler` (unstable) |

## 클라이언트 메서드 (에이전트 → 클라이언트)

| 메서드 | Go 인터페이스 |
| --- | --- |
| `session/update`, `session/request_permission` | `Client` (필수) |
| `fs/read_text_file` | `FileReader` |
| `fs/write_text_file` | `FileWriter` |
| `terminal/*` | `TerminalHandler` |
| `elicitation/create`, `elicitation/complete` | `ElicitationHandler` |
| `mcp/message` (요청) | `MCPProvider` (unstable) |

`$/cancel_request`는 연결이 직접 처리합니다.

## ACP v2 (`acp2`, 초안)

### 에이전트 메서드 (클라이언트 → 에이전트)

| 메서드 | Go 인터페이스 |
| --- | --- |
| `initialize`, `session/new`, `session/prompt`, `session/cancel` | `Agent` (필수) |
| `auth/login`, `auth/logout` | `AuthHandler` |
| `session/list` | `SessionLister` |
| `session/delete` | `SessionDeleter` |
| `session/fork` | `SessionForker` |
| `session/resume` | `SessionResumer` |
| `session/close` | `SessionCloser` |
| `session/set_config_option` | `SessionConfigOptionSetter` |
| `providers/*` | `ProviderManager` (unstable) |
| `nes/*` | `NesHandler` (unstable) |
| `document/did*` | `DocumentHandler` (unstable) |
| `mcp/message` (알림) | `MCPMessageHandler` (unstable) |

### 클라이언트 메서드 (에이전트 → 클라이언트)

| 메서드 | Go 인터페이스 |
| --- | --- |
| `session/update`, `session/request_permission` | `Client` (필수) |
| `mcp/message` (요청) | `MCPProvider` (unstable) |
| `elicitation/create`, `elicitation/complete` | `ElicitationHandler` |

### 턴 처리

v2에는 `fs/*`·`terminal/*` 메서드가 없으므로(파일·셸 접근은 MCP 경유) `TerminalHandle`은 v1 패키지에만
있습니다. v2에서 prompt 응답은 메시지 접수만 뜻하며, `Turn`은 에이전트가 idle 상태를 보고할 때 끝납니다.

턴이 겹치는 프롬프트는 버전별 규칙을 양쪽이 똑같이 따릅니다. v1 세션은 한 번에 한 턴만 돌리므로
`ClientSession.Prompt`와 `SessionManager.RunTurn`(또는 `BeginTurn`) 모두 두 번째 프롬프트를
`acp.ErrTurnInProgress`(`-32600`)로 거절합니다. v2에서는 프롬프트가 진행 중인 작업에 합류할 수 있습니다.
`Prompt`는 메시지가 접수되면 `(*Turn, MessageID, error)`를 돌려주며 진행 중인 턴에 합류하고,
`SessionManager.StartTurn`(또는 `JoinTurn`)은 에이전트에게 진행 중인 턴의 context를 넘겨주며 작업 앞뒤로
running과 idle을 보고합니다.

### 취소 순서

프롬프트 뒤에 보낸 `session/cancel`은 항상 그 프롬프트의 턴에 닿습니다. `ClientSession.Prompt`는 프롬프트를
전송 대기열에 넣은 뒤에야 돌아오므로 그 뒤의 `Cancel`은 프롬프트 다음에 전송되고, 에이전트 핸들러가
턴을 시작하기 전에 도착한 cancel도 그 턴을 `acp.ErrTurnCancelled`로 취소된 채 시작하게
합니다.
