// Run against the pinned upstream checkout using the temporary Cargo manifest
// documented in PROVENANCE.md. Ordinary Go tests do not compile this harness.
use hdr10plus::metadata::{PeakBrightnessSource, VariablePeakBrightness};
use hdr10plus::metadata_json::{Hdr10PlusJsonMetadata, MetadataJsonRoot};
use serde_json::{json, Value};

mod utils {
    include!(concat!(env!("HDR10PLUS_REFERENCE"), "/src/utils.rs"));
}

fn stats(frames: &[Hdr10PlusJsonMetadata], source: PeakBrightnessSource) -> Value {
    let samples: Vec<_> = frames.iter().map(|f| {
        (utils::nits_to_pq(f.luminance_parameters.average_rgb as f64 / 10.0),
         utils::nits_to_pq(f.peak_brightness_nits(source).unwrap()))
    }).collect();
    let max_avg = samples.iter().map(|v| v.0).reduce(f64::max).unwrap();
    let max_peak = samples.iter().map(|v| v.1).reduce(f64::max).unwrap();
    let mean_avg = samples.iter().map(|v| v.0).sum::<f64>() / samples.len() as f64;
    let mean_peak = samples.iter().map(|v| v.1).sum::<f64>() / samples.len() as f64;
    json!({"average_max": utils::pq_to_nits(max_avg), "average_mean": utils::pq_to_nits(mean_avg),
           "peak_max": utils::pq_to_nits(max_peak), "peak_mean": utils::pq_to_nits(mean_peak)})
}

fn sources() -> [PeakBrightnessSource; 4] {
    [PeakBrightnessSource::Histogram, PeakBrightnessSource::Histogram99,
     PeakBrightnessSource::MaxScl, PeakBrightnessSource::MaxSclLuminance]
}

fn main() {
    let nits = [0.0, 0.01, 0.1, 0.5, 1.0, 2.5, 5.0, 10.0, 25.0, 50.0, 100.0,
                200.0, 400.0, 600.0, 1000.0, 2000.0, 4000.0, 10000.0, u32::MAX as f64 / 10.0];
    let pq: Vec<_> = nits.iter().map(|&v| {
        let p = utils::nits_to_pq(v);
        json!({"nits": v, "pq": p, "round_trip": utils::pq_to_nits(p)})
    }).collect();
    let mut frame = Hdr10PlusJsonMetadata::default();
    frame.luminance_parameters.average_rgb = 1234;
    frame.luminance_parameters.max_scl = vec![3000, 1000, 2000];
    frame.luminance_parameters.luminance_distributions.distribution_values = vec![9000, 100, 5000];
    let estimators: Vec<_> = sources().into_iter().map(|s| frame.peak_brightness_nits(s).unwrap()).collect();
    let mut files = Vec::new();
    for path in std::env::args().skip(1) {
        let root = MetadataJsonRoot::from_file(&path).unwrap();
        let n = root.scene_info.len();
        let mut ranges = vec![(0, n-1)];
        if n > 10 { ranges.push((2, 9)); }
        let checks: Vec<_> = ranges.into_iter().map(|(start, end)| {
            json!({"start": start, "end": end,
                "sources": sources().into_iter().map(|s| stats(&root.scene_info[start..=end], s)).collect::<Vec<_>>()})
        }).collect();
        files.push(json!({"name": std::path::Path::new(&path).file_name().unwrap().to_str().unwrap(),
            "frames": n, "profile": root.info.profile, "scenes": root.scene_info_summary.scene_frame_numbers.len(), "checks": checks}));
    }
    let synthetic: Vec<_> = [("zero", vec![0, 0]), ("constant", vec![1000, 1000]),
        ("variation", vec![0, 10000]), ("above_peak", vec![10000, 5000])].into_iter().map(|(name, averages)| {
        let frames: Vec<_> = averages.into_iter().map(|average| {
            let mut f = Hdr10PlusJsonMetadata::default();
            f.luminance_parameters.average_rgb = average;
            f.luminance_parameters.luminance_distributions.distribution_values = vec![if name == "above_peak" {1000} else {average}];
            f
        }).collect();
        json!({"name": name, "statistics": stats(&frames, PeakBrightnessSource::Histogram)})
    }).collect();
    println!("{}", serde_json::to_string_pretty(&json!({"pq": pq, "estimators": estimators, "files": files, "synthetic": synthetic})).unwrap());
}
