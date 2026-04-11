sudo cp /home/ubuntu/ucl-src/scripts/aws-ibft/polygon-edge.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now polygon-edge