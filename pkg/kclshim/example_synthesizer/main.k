import json
import pkg

input_json = """{"a":1}"""
input = json.decode(input_json)
output = pkg.Synthesize(input)