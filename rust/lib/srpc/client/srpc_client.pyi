from typing import Callable, List

type JsonStr = str

class SrpcClientConfig:
    def __init__(self, host: str, port: int, path: str, cert: str, key: str) -> None: ...
    async def connect(self) -> "ConnectedSrpcClient": ...

class ConnectedSrpcClient:
    async def send_message(self, message: str) -> None: ...
    async def send_message_and_check(self, message: str) -> None: ...
    async def receive_message(self, expect_empty: bool, should_continue: bool) -> List[str]: ...
    async def receive_message_cb(self, expect_empty: bool, should_continue: Callable[[str], bool]) -> List[str]: ...
    async def send_json(self, payload: str) -> None: ...
    async def send_json_and_check(self, payload: str) -> None: ...
    async def receive_json(self, should_continue: bool) -> List[str]: ...
    async def receive_json_cb(self, should_continue: Callable[[str], bool]) -> List[str]: ...
    async def request_reply(self, message: str, payload: JsonStr) -> JsonStr: ...
