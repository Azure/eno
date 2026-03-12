import eno
import pkg

input: eno.ResourceList = option("input")
output: [any] = pkg.Synthesize(input)
