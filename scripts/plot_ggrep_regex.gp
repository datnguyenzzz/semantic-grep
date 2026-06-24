# Gnuplot script to plot ggrep vs other search engines performance scale for Regex Search
# X-axis: Total Matched Lines | Y-axis: Latency (ms)

set terminal pngcairo size 1000,700 enhanced font "Verdana,10"
set output 'results/ggrep_bench_regex_chart.png'

set title "Regex Search Latency vs. Scenario Index (1.2GB Codebase)" font "Verdana,14,Bold"
set xlabel "Query Scenario Index (1 to 50)" font "Verdana,11,Bold"
set ylabel "Wall-Clock Time Spent (ms)" font "Verdana,11,Bold"

set grid xtics ytics linestyle 1 lc rgb '#E0E0E0' lt 1 lw 1
set key top left box font "Verdana,9" spacing 1.2

# Style lines for each engine
set style line 1 lc rgb '#E41A1C' lt 1 lw 2.5 pt 7 ps 1.5 # crimson (Our ggrep)
set style line 2 lc rgb '#377EB8' lt 1 lw 2.5 pt 5 ps 1.5 # blue (bep/grrep)
set style line 3 lc rgb '#4DAF4A' lt 1 lw 2.5 pt 9 ps 1.5 # green (burntsushi/rg)
set style line 4 lc rgb '#984EA3' lt 1 lw 2.5 pt 11 ps 1.5 # purple (git-grep)
set style line 5 lc rgb '#FF7F00' lt 1 lw 2.5 pt 13 ps 1.5 # orange (Native OS grep)

plot 'results/ggrep_bench_regex_ggrep.dat' using 1:2 with linespoints ls 1 title "Our ggrep (Rx)", \
     'results/ggrep_bench_regex_grrep.dat' using 1:2 with linespoints ls 2 title "bep/grrep (Rx)", \
     'results/ggrep_bench_regex_ripgrep.dat' using 1:2 with linespoints ls 3 title "burntsushi/rg (Rx)", \
     'results/ggrep_bench_regex_gitgrep.dat' using 1:2 with linespoints ls 4 title "git-grep (Rx)", \
     'results/ggrep_bench_regex_osgrep.dat' using 1:2 with linespoints ls 5 title "Native OS grep (Rx)"
