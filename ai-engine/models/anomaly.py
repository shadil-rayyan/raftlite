import numpy as np
from sklearn.ensemble import IsolationForest

class AnomalyDetector:
    def __init__(self, window_size: int = 1000, threshold: float = 0.4):
        self.window_size = window_size
        self.threshold = threshold
        self.buffer: list[dict] = []
        self.model = IsolationForest(
            contamination=0.1, random_state=42, n_estimators=50
        )

    def feed(self, frames: list[dict]) -> list[str]:
        """Feed telemetry frames, return list of anomalous IPs."""
        self.buffer.extend(frames)
        if len(self.buffer) > self.window_size:
            self.buffer = self.buffer[-self.window_size:]

        if len(self.buffer) < 100:
            return []

        features = np.array([
            [f["timestamp_ns"] % 1_000_000,
             f["latency_ns"],
             f["route_id"]]
            for f in self.buffer
        ], dtype=np.float64)

        preds = self.model.fit_predict(features)
        anomalies = []
        for i, p in enumerate(preds):
            if p == -1:
                score = self.model.score_samples(features[i:i+1])[0]
                if score < -self.threshold:
                    anomalies.append(self.buffer[i]["ip"])

        # deduplicate
        return list(set(anomalies))
