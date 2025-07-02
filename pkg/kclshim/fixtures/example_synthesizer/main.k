import eno
import pkg

input: eno.ResourceList = option("input")
output: eno.ResourceList = pkg.Synthesize(input)
