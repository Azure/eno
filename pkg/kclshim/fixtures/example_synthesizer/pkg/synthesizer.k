import eno

Synthesize: (eno.ResourceList) -> [any] = lambda input: eno.ResourceList -> [any] {
    image_base = [item.data.image_base for item in input.items if item.metadata.name == "some-config"][0]

    output = [
        GetDeployment(image_base),
        my_service_account,
    ]
}
