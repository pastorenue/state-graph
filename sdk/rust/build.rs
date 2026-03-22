fn main() {
    tonic_build::configure()
        .build_server(false) // client only — SDK never serves gRPC
        .compile(
            &[
                "../../proto/kflow/v1/runner.proto",
                "../../proto/kflow/v1/types.proto",
            ],
            &["../../proto"],
        )
        .expect("proto compile failed");
}
