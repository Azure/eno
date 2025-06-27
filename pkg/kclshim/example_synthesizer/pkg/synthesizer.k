Synthesize: (ResourceList) -> ResourceList = lambda input: ResourceList -> ResourceList {
    mcr_base = [item.data.mcr_base for item in input.items if item.metadata.name == "some-config"][0]

    output = ResourceList {
        items: [
            GetDeployment(mcr_base),
            my_service_account,
        ]
    }
}