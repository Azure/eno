import eno

Synthesize: (eno.ResourceList) -> eno.ResourceList = lambda input: eno.ResourceList -> eno.ResourceList {
    image_base = [item.data.image_base for item in input.items if item.metadata.name == "some-config"][0]

    output = eno.ResourceList {
        items: [
            GetDeployment(image_base),
            my_service_account,
        ]
    }
}