use std::io::Read;

fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).unwrap();
    let sum: i64 = input.split_whitespace().map(|x| x.parse::<i64>().unwrap()).sum();
    println!("{}", sum);
}
