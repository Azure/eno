import json
import pkg

input: pkg.ResourceList = option("input")
output: pkg.ResourceList = pkg.Synthesize(input)
