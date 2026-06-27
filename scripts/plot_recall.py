#!/usr/bin/env python3
import os
import json
import matplotlib.pyplot as plt

RESULTS_DIR = os.path.join(os.path.dirname(__file__), "..", "results")

def plot_dimension(dim):
    faiss_path = os.path.join(RESULTS_DIR, f"faiss_recall_d{dim}_4bit.json")
    tq_path = os.path.join(RESULTS_DIR, f"tq_recall_d{dim}_4bit.json")

    faiss_data = {}
    if os.path.exists(faiss_path):
        with open(faiss_path, "r") as f:
            faiss_data = json.load(f).get("faiss_recalls", {})
    else:
        print(f"  ⚠️  FAISS result not found at: {faiss_path}")

    tq_data = {}
    if os.path.exists(tq_path):
        with open(tq_path, "r") as f:
            tq_data = json.load(f).get("tq_recalls", {})
    else:
        print(f"  ⚠️  TurboQuant result not found at: {tq_path}")

    if not faiss_data and not tq_data:
        print(f"⚠️  No recall data found for d={dim}. Skipping chart generation.")
        return

    # Sort k values numerically
    k_values = sorted([int(k) for k in faiss_data.keys()]) if faiss_data else sorted([int(k) for k in tq_data.keys()])
    
    faiss_y = [faiss_data.get(str(k), 0.0) for k in k_values] if faiss_data else []
    tq_y = [tq_data.get(str(k), 0.0) for k in k_values] if tq_data else []

    plt.figure(figsize=(8, 6))

    if faiss_y:
        plt.plot(k_values, faiss_y, marker="o", color="crimson", linestyle="-", linewidth=2, label="FAISS PQ (m=dim/2, 8-bit)")
    if tq_y:
        plt.plot(k_values, tq_y, marker="s", color="royalblue", linestyle="--", linewidth=2, label="TurboQuant (4-bit)")

    plt.xlabel("k (Top-k Candidates)", fontsize=12)
    plt.ylabel("Recall-1-@k (Accuracy)", fontsize=12)
    plt.title(f"Recall-1-@k Accuracy Comparison (OpenAI d={dim})", fontsize=14, fontweight="bold")
    plt.xscale("log", base=2)
    plt.xticks(k_values, labels=[str(k) for k in k_values])
    plt.ylim([0.0, 1.05])
    plt.grid(True, which="both", linestyle="--", alpha=0.5)
    plt.legend(fontsize=11, loc="lower right")

    png_path = os.path.join(RESULTS_DIR, f"recall_chart_d{dim}_4bit.png")
    plt.savefig(png_path, dpi=300, bbox_inches="tight")
    plt.close()
    print(f"✓ Successfully generated recall chart: {png_path}")

def plot_effectiveness_for_dim(dim):
    json_path = os.path.join(RESULTS_DIR, f"hybrid_recall_comparison_d{dim}_4bit.json")
    if not os.path.exists(json_path):
        print(f"⚠️  Effectiveness JSON not found at: {json_path}. Skipping d={dim}.")
        return

    with open(json_path, "r") as f:
        data = json.load(f)

    # Extract metrics
    methods = ["semantic", "grep", "semantic_grep"]
    method_labels = {
        "semantic": "Semantic",
        "grep": "Grep",
        "semantic_grep": "Semantic Grep"
    }
    colors = {
        "semantic": "crimson",
        "grep": "darkgray",
        "semantic_grep": "royalblue"
    }

    metrics = ["Recall@1", "Recall@3", "Recall@5", "MRR"]
    
    # Compile raw values
    scores = {}
    for m in methods:
        m_data = data.get(m, {})
        scores[m] = [
            m_data.get("recall_1", 0.0),
            m_data.get("recall_3", 0.0),
            m_data.get("recall_5", 0.0),
            m_data.get("mrr", 0.0)
        ]

    # Create Grouped Bar Chart
    import numpy as np
    x = np.arange(len(metrics))
    width = 0.2  # width of each bar

    plt.figure(figsize=(11, 6))

    # Symmetrical offsets for 3 grouped bars
    offsets = [-width, 0, width]

    # Plot bars dynamically
    for i, m in enumerate(methods):
        plt.bar(x + offsets[i], scores[m], width, label=method_labels[m], color=colors[m])

    # Format Chart
    plt.xlabel("Evaluation Metrics", fontsize=12)
    plt.ylabel("Score (0.0 to 1.0)", fontsize=12)
    plt.title(f"Search Effectiveness Comparison (OpenAI d={dim})", fontsize=14, fontweight="bold")
    plt.xticks(x, metrics, fontsize=11)
    plt.ylim([0.0, 1.05])
    plt.grid(True, axis="y", linestyle="--", alpha=0.5)
    
    # Add values on top of the bars
    for i, m in enumerate(methods):
        offset = offsets[i]
        for j, val in enumerate(scores[m]):
            plt.text(j + offset, val + 0.01, f"{val:.2f}", ha="center", va="bottom", fontsize=8, fontweight="bold")

    # Place legend cleanly above the chart to prevent clashing with the vertical bars
    plt.legend(fontsize=11, loc="upper center", bbox_to_anchor=(0.5, 1.12), ncol=3, frameon=False)
    plt.tight_layout()

    png_path = os.path.join(RESULTS_DIR, f"hybrid_effectiveness_chart_d{dim}_4bit.png")
    plt.savefig(png_path, dpi=300, bbox_inches="tight")
    plt.close()
    print(f"✓ Successfully generated hybrid effectiveness chart: {png_path}")

def main():
    import sys
    os.makedirs(RESULTS_DIR, exist_ok=True)
    
    # Check if 'effectiveness' is passed as a CLI argument
    if len(sys.argv) > 1 and "effectiveness" in sys.argv:
        print("📊 Plotting large-scale hybrid search effectiveness bar charts...")
        plot_effectiveness_for_dim(1536)
        plot_effectiveness_for_dim(3072)
    else:
        print("📈 Plotting FAISS vs. TurboQuant recall comparison curves...")
        plot_dimension(1536)
        plot_dimension(3072)

if __name__ == "__main__":
    main()
