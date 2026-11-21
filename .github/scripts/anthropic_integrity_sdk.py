"""Official client accumulator against an isolated GatewayHandler loopback server."""
import json
import sys
from urllib.parse import urlparse

import openai

endpoint, scenario, stream_flag = sys.argv[1:]
assert urlparse(endpoint).hostname in ('127.0.0.1', 'localhost')
streaming = stream_flag == 'true'
with openai.DefaultHttpx2Client(trust_env=False, timeout=20) as transport:
    with openai.OpenAI(api_key='synthetic-fixture', base_url=endpoint + '/v1', max_retries=0, http_client=transport) as client:
        messages = [{'role': 'user', 'content': scenario}]
        tools = [{'type': 'function', 'function': {'name': 'weather', 'parameters': {
            'type': 'object', 'properties': {'city': {'type': 'string'}}, 'required': ['city']}}}]

        def complete(history):
            if streaming:
                with client.chat.completions.stream(model='claude-sonnet-4.5', messages=history, tools=tools) as response:
                    return response.get_final_completion()
            return client.chat.completions.create(model='claude-sonnet-4.5', messages=history, tools=tools)

        if scenario in ('missing', 'partial_error', 'partial_protocol'):
            try:
                complete(messages)
            except openai.APIError:
                pass
            else:
                raise AssertionError('official accumulator accepted incomplete stream as success')
        else:
            response = complete(messages)
            assert response.model == 'claude-sonnet-4.5'
            assert response.usage is not None
            assert response.usage.prompt_tokens == (0 if scenario == 'zero' else 12)
            assert response.usage.completion_tokens == (0 if scenario == 'zero' else 7)
            if scenario == 'tool':
                choice = response.choices[0]
                assert choice.finish_reason == 'tool_calls'
                call, = choice.message.tool_calls
                assert call.id == 'call_fixture' and call.function.name == 'weather'
                assert json.loads(call.function.arguments) == {'city': 'Paris'}
                messages += [choice.message.model_dump(exclude_none=True),
                             {'role': 'tool', 'tool_call_id': call.id, 'content': 'sunny'}]
                response = complete(messages)
            assert response.choices[0].message.content == 'partial'
            assert response.choices[0].finish_reason == 'stop'
print(json.dumps({'SDK_PASS': True, 'sdk': openai.__version__, 'scenario': scenario, 'stream': streaming}))
